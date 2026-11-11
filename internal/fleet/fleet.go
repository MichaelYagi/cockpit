// Package fleet keeps the list of machines a fleet console watches (D30).
// It is a client-side file: no agent changes, no central server.
package fleet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/MichaelYagi/cockpit/pkg/client"
)

// FileName is the fleet list in the user's configuration directory.
const FileName = "fleet.yaml"

// MaxMachines bounds the list; pull polling is designed for tens of
// machines, not thousands (D30).
const MaxMachines = 200

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Machine is one enrolled agent. The key itself is never stored here.
type Machine struct {
	Name           string    `yaml:"name"`
	URL            string    `yaml:"url"`
	KeyFile        string    `yaml:"key_file,omitempty"`
	KeyEnv         string    `yaml:"key_env,omitempty"`
	TLSFingerprint string    `yaml:"tls_fingerprint,omitempty"`
	TLSCA          string    `yaml:"tls_ca,omitempty"`
	MachineID      string    `yaml:"machine_id"`
	AddedAt        time.Time `yaml:"added_at"`
}

// List is the fleet file.
type List struct {
	Machines []Machine `yaml:"machines"`
}

// ErrIdentityChanged means a machine answered with a different machine ID
// than at enrollment (D30). The certificate pin catches a changed
// certificate before any request is sent.
var ErrIdentityChanged = errors.New("machine identity changed")

// Load reads the list. A missing file is an empty list. The file must not
// be readable by group or others, since it names key files.
func Load(path string) (*List, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &List{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := private(f, path); err != nil {
		return nil, err
	}
	var l List
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&l); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, m := range l.Machines {
		if err := m.validate(); err != nil {
			return nil, fmt.Errorf("%s: machine %d: %w", path, i+1, err)
		}
	}
	return &l, nil
}

// Save writes the list atomically with mode 0600.
func (l *List) Save(path string) error {
	sort.Slice(l.Machines, func(i, j int) bool { return l.Machines[i].Name < l.Machines[j].Name })
	var b bytes.Buffer
	b.WriteString("# Cockpit fleet (D30). Managed by cockpit fleet add/remove. Keys are not stored here.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(l); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fleet-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Get returns the machine with this name.
func (l *List) Get(name string) (Machine, bool) {
	for _, m := range l.Machines {
		if m.Name == name {
			return m, true
		}
	}
	return Machine{}, false
}

// Put adds m, or replaces a machine with the same name when replace is set.
func (l *List) Put(m Machine, replace bool) error {
	if err := m.validate(); err != nil {
		return err
	}
	for i, x := range l.Machines {
		if x.Name == m.Name {
			if !replace {
				return fmt.Errorf("%s is already enrolled; use --replace to update it", m.Name)
			}
			l.Machines[i] = m
			return nil
		}
	}
	if len(l.Machines) >= MaxMachines {
		return fmt.Errorf("a fleet holds at most %d machines", MaxMachines)
	}
	l.Machines = append(l.Machines, m)
	return nil
}

// Remove deletes a machine; it reports whether one was removed.
func (l *List) Remove(name string) bool {
	for i, m := range l.Machines {
		if m.Name == name {
			l.Machines = append(l.Machines[:i], l.Machines[i+1:]...)
			return true
		}
	}
	return false
}

func (m Machine) validate() error {
	switch {
	case !namePattern.MatchString(m.Name):
		return fmt.Errorf("name %q must be lower case letters, digits, '.', '_' or '-' (at most 64)", m.Name)
	case m.URL == "":
		return errors.New("url is required")
	case (m.KeyFile == "") == (m.KeyEnv == ""):
		return errors.New("exactly one of key_file and key_env is required")
	case m.KeyFile != "" && !filepath.IsAbs(m.KeyFile):
		return fmt.Errorf("key_file %q must be an absolute path", m.KeyFile)
	case m.TLSFingerprint != "" && m.TLSCA != "":
		return errors.New("use tls_fingerprint or tls_ca, not both")
	}
	if m.TLSFingerprint != "" {
		if _, err := client.NormalizeFingerprint(m.TLSFingerprint); err != nil {
			return err
		}
	}
	return nil
}

// Key reads the machine's API key from its file (which must be private) or
// environment variable.
func (m Machine) Key(lookupEnv func(string) (string, bool)) (string, error) {
	if m.KeyEnv != "" {
		v, ok := lookupEnv(m.KeyEnv)
		if !ok || v == "" {
			return "", fmt.Errorf("%s: environment variable %s is not set", m.Name, m.KeyEnv)
		}
		return strings.TrimSpace(v), nil
	}
	f, err := os.Open(m.KeyFile)
	if err != nil {
		return "", fmt.Errorf("%s: %w", m.Name, err)
	}
	defer f.Close()
	if err := private(f, m.KeyFile); err != nil {
		return "", fmt.Errorf("%s: %w", m.Name, err)
	}
	b := make([]byte, 256)
	n, _ := f.Read(b)
	return strings.TrimSpace(string(b[:n])), nil
}

// Client builds an API client that trusts the machine as enrolled.
func (m Machine) Client(lookupEnv func(string) (string, bool), opts ...client.Option) (*client.Client, error) {
	key, err := m.Key(lookupEnv)
	if err != nil {
		return nil, err
	}
	if m.TLSFingerprint != "" {
		opts = append(opts, client.WithPinnedCertificate(m.TLSFingerprint))
	}
	if m.TLSCA != "" {
		pem, err := os.ReadFile(m.TLSCA)
		if err != nil {
			return nil, fmt.Errorf("%s: read CA: %w", m.Name, err)
		}
		opts = append(opts, client.WithCAPEM(pem))
	}
	return client.NewTCP(m.URL, key, opts...)
}

// Verify contacts the machine and checks that it is the one enrolled.
func Verify(ctx context.Context, m Machine, c *client.Client) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if m.MachineID != "" && st.MachineID != m.MachineID {
		return fmt.Errorf("%w: %s now reports machine ID %s, enrolled as %s. If it was reinstalled, enroll it again with: cockpit fleet add --replace",
			ErrIdentityChanged, m.Name, st.MachineID, m.MachineID)
	}
	return nil
}

// private refuses files readable by group or others.
func private(f *os.File, path string) error {
	if runtime.GOOS == "windows" {
		return nil // ACLs, not mode bits; see docs/SECURITY.md
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s has mode %04o; it must not be accessible by group or others (chmod 600 %s)", path, perm, path)
	}
	return nil
}
