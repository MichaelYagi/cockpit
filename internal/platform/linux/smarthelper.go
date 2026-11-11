//go:build linux

package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MichaelYagi/cockpit/internal/collector"
)

// The SMART helper (D20) runs smartctl as root from cockpit-smart.service
// and leaves one result file per disk for the unprivileged agent.
const (
	// SMARTResultMaxAge is how old helper results may be before the agent
	// reports them as stale.
	SMARTResultMaxAge = 3 * time.Hour
	smartResultSuffix = ".json"
	smartResultMax    = maxToolOutput + 4096
)

// smartResultFile is the helper's file format.
type smartResultFile struct {
	Device      string          `json:"device"`
	CollectedAt time.Time       `json:"collected_at"`
	ExitCode    int             `json:"exit_code"`
	Smartctl    json.RawMessage `json:"smartctl"` // null if the output was not JSON
}

// SMARTHelperOptions configure WriteSMARTResults.
type SMARTHelperOptions struct {
	Dir string // created 0750 if missing; must be owned by the caller
	GID int    // group for the directory and files; -1 keeps the default
}

// WriteSMARTResults runs smartctl for each disk and writes the results to
// opts.Dir, replacing each file atomically and removing files for disks
// that are gone. It returns the number of files written.
//
// The parent of opts.Dir belongs to the agent's user, so the directory is
// opened without following symlinks and must be owned by the effective
// user and not writable by others; all writes go through the directory
// descriptor. A tampered directory is refused, never repaired.
func WriteSMARTResults(ctx context.Context, env collector.Env, opts SMARTHelperOptions) (int, error) {
	return writeSMARTResults(ctx, env, opts, realRun)
}

func writeSMARTResults(ctx context.Context, env collector.Env, opts SMARTHelperOptions, run runFunc) (int, error) {
	tool, ok := "", false
	if env.Tools != nil {
		tool, ok = env.Tools.Find("smartctl")
	}
	if !ok {
		return 0, errors.New(smartNotInstalled.Reason)
	}
	disks, err := listDisks(env.SysRoot)
	if err != nil {
		return 0, fmt.Errorf("list disks: %w", err)
	}
	dfd, err := openResultsDir(opts.Dir, opts.GID)
	if err != nil {
		return 0, err
	}
	defer unix.Close(dfd)

	keep := map[string]bool{}
	written := 0
	for _, d := range disks {
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		if !safeName(d) {
			continue
		}
		stdout, code, err := run(ctx, tool, "--json=c", "--info", "--health", "--attributes", "--nocheck=standby", filepath.Join(env.DevRoot, d))
		if err != nil {
			env.Logger.Warn("smartctl failed", "device", d, "error", err)
			continue
		}
		rec := smartResultFile{Device: d, CollectedAt: env.Now().UTC(), ExitCode: code, Smartctl: json.RawMessage("null")}
		if json.Valid(stdout) {
			rec.Smartctl = stdout
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return written, err
		}
		name := d + smartResultSuffix
		if err := writeAt(dfd, name, data, opts.GID); err != nil {
			return written, fmt.Errorf("write %s: %w", name, err)
		}
		keep[name] = true
		written++
	}
	return written, removeOthers(dfd, keep)
}

// safeName accepts kernel block device names only.
func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\x00") && len(s) <= 64
}

// openResultsDir opens (creating if needed) the results directory and
// checks it cannot have been prepared by someone else.
func openResultsDir(dir string, gid int) (int, error) {
	if !filepath.IsAbs(dir) {
		return -1, fmt.Errorf("results directory %q must be an absolute path", dir)
	}
	parent, base := filepath.Split(filepath.Clean(dir))
	pfd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", parent, err)
	}
	defer unix.Close(pfd)
	if err := unix.Mkdirat(pfd, base, 0o750); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("create %s: %w", dir, err)
	}
	dfd, err := unix.Openat(pfd, base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s (it must be a directory, not a symlink): %w", dir, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(dfd, &st); err != nil {
		unix.Close(dfd)
		return -1, err
	}
	if int(st.Uid) != os.Geteuid() || st.Mode&0o022 != 0 {
		unix.Close(dfd)
		return -1, fmt.Errorf("refusing to use %s: it must be owned by uid %d and not writable by group or others (owner %d, mode %o); remove it and run the helper again",
			dir, os.Geteuid(), st.Uid, st.Mode&0o7777)
	}
	if gid >= 0 {
		if err := unix.Fchown(dfd, -1, gid); err != nil {
			unix.Close(dfd)
			return -1, fmt.Errorf("set group of %s: %w", dir, err)
		}
	}
	if err := unix.Fchmod(dfd, 0o750); err != nil {
		unix.Close(dfd)
		return -1, err
	}
	return dfd, nil
}

// writeAt replaces dfd/name atomically with a 0640 file.
func writeAt(dfd int, name string, data []byte, gid int) error {
	tmp := "." + name + ".tmp"
	_ = unix.Unlinkat(dfd, tmp, 0) // a leftover from an interrupted run
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o640)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	err = func() error {
		if gid >= 0 {
			if err := f.Chown(-1, gid); err != nil {
				return err
			}
		}
		if err := f.Chmod(0o640); err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		return f.Sync()
	}()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	return unix.Renameat(dfd, tmp, dfd, name)
}

// removeOthers deletes result files that were not written this run.
func removeOthers(dfd int, keep map[string]bool) error {
	dup, err := unix.Dup(dfd)
	if err != nil {
		return err
	}
	d := os.NewFile(uintptr(dup), "smart")
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for _, n := range names {
		if strings.HasSuffix(n, smartResultSuffix) && !keep[n] {
			if err := unix.Unlinkat(dfd, n, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return err
			}
		}
	}
	return nil
}

// readHelperResults reads helper files for disks. found reports whether
// any file exists (the helper is in use); stale counts files older than
// SMARTResultMaxAge.
func readHelperResults(dir string, disks []string, now time.Time) (out []smartResultFile, found bool, stale int) {
	if dir == "" {
		return nil, false, 0
	}
	for _, d := range disks {
		if !safeName(d) {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, d+smartResultSuffix), os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, smartResultMax))
		f.Close()
		var rec smartResultFile
		if err != nil || json.Unmarshal(data, &rec) != nil || rec.Device != d {
			continue
		}
		found = true
		if now.Sub(rec.CollectedAt) > SMARTResultMaxAge {
			stale++
			continue
		}
		out = append(out, rec)
	}
	return out, found, stale
}
