// Package mcp adapts Model Context Protocol servers into agentcore tools. It
// owns the MCP client SDK and its types and exposes only []ac.Tool plus a
// closer; no SDK type crosses this boundary.
//
// Trust is the caller's allowlist: Tools only connects to the servers it is
// handed. Every adapted tool is non-safe (it does not implement gate.SafeTool),
// so a jess gate confirm-gates and ledgers each call unless the host opted
// into AllowAll.
package mcp
