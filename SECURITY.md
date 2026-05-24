# Security

## Reporting a vulnerability

Email security reports to `guy@grigsby.dev` with subject prefix
`[jess security]`. Include:

- Affected version (commit or tagged release)
- Reproducer
- Impact assessment

Expect an acknowledgment within 72 hours. Fix-and-disclose timing
will be coordinated with you.

Please don't file public issues for security-sensitive reports.
General bug reports without security implications are fine on the
issue tracker.

## What's in scope

- Code execution via memory injection (e.g. a `Recall` payload that
  escapes the prompt-as-data boundary and executes as instructions).
  This is the agent-memory equivalent of SQL injection and we want
  to know if you find it.
- Path traversal via `JSONLStore` or skill loader filesystem walks.
- Sensitive data leaking into stored memory entries through the
  default redaction (currently: none — see "Out of scope" below).

## Out of scope

These are known limitations, not vulnerabilities:

- **No redaction of stored memory contents.** Entries store user
  text verbatim. If your agent's `remember` tool gets called with
  a credit card number, it'll be written to disk. Hosts that need
  redaction add it at write time (wrap `RememberTool` or implement
  a custom `Store` that redacts in `Append`).
- **No encryption at rest** for `JSONLStore` or `ChromemStore`.
  File-system permissions are the protection.
- **Embedding models from HuggingFace** are downloaded over HTTPS
  without separate signature verification beyond what HuggingFace's
  own infrastructure provides.

## Dependency security

`jess` depends on a small set of MIT/Apache/MPL libraries. We don't
audit them ourselves; rely on `govulncheck` against your final
binary.

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```
