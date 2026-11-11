-- D27: network addresses, routes and DNS move to the new network:read
-- scope. Keys that could read them through metrics:read keep that access.
-- Scopes are stored space-separated and sorted, and "network:read" sorts
-- directly after "metrics:read", so the replacement keeps the order.
UPDATE api_keys
SET scopes = REPLACE(scopes, 'metrics:read', 'metrics:read network:read')
WHERE (' ' || scopes || ' ') LIKE '% metrics:read %'
  AND (' ' || scopes || ' ') NOT LIKE '% network:read %';
