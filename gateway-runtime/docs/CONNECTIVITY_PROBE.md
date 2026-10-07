# Read-only connectivity probe

This utility checks the mini PC's path to the central AuditChain services without starting the copied Gateway runtime.

It performs only:

1. PostgreSQL connection ping.
2. A parameterized `SELECT` for one already-anchored log owned by `CONNECTIVITY_CLIENT_ID`.
3. Fabric `EvaluateTransaction("QueryMerkleRoot", anchorID)` and comparison of the returned anchor ID and Merkle root with PostgreSQL.

It does not call `AutoMigrate`, run schema repair, start Kafka consumers/workers, or submit a Fabric transaction. The PostgreSQL account should be read-only. A plain `SELECT` grant on `audit_logs` is not tenant isolation: for production use, scope access with PostgreSQL row-level security or a client-specific restricted view/function. The Fabric identity should have evaluate/query permission only.

## Configure and run

From `gateway-runtime`, copy `.env.connectivity.example` to `.env.connectivity`, then replace placeholders locally. Do not commit `.env.connectivity`, Fabric private keys, or real credentials. Set the DB host and Fabric peer address to the Besu server's Tailscale address and the ports actually published/reachable there. Ensure the existing chaincode's `QueryMerkleRoot` read function is available to this identity.

Run:

```sh
go run ./cmd/connectivity-check
```

When `FABRIC_PEER_ENDPOINT` uses an IP address (for example, a Tailscale IP) but the peer TLS certificate contains only DNS SANs, set `FABRIC_PEER_TLS_SERVER_NAME` to one of those exact DNS names (for example, `peer0.org1.audit.example.com`). The connection still goes to the configured endpoint; this setting supplies the TLS verification/SNI name. Certificate verification remains enabled—do not use an insecure TLS option or replace the peer certificate with an unverified one.

The command exits non-zero on a connection failure, missing anchor, malformed Fabric response, or mismatch. It prints only pass/fail summaries and does not print the DSN or credentials.

If the client has no anchored audit log yet, set `CONNECTIVITY_ANCHOR_ID` to a known valid anchor belonging to that client. The read query still confirms the anchor and expected Merkle root from PostgreSQL before evaluating Fabric.

## Expected successful output

```text
PostgreSQL: PASS (koneksi berhasil; tidak ada migrasi/schema write)
Fabric: PASS (Evaluate/read-only; anchor dan Merkle root cocok dengan PostgreSQL)
```

This proves connectivity and a read-only ledger comparison from the machine where the command ran. It does not prove the full Gateway runtime is safe to deploy or that Kafka ingestion/anchoring is correctly tenant-scoped.
