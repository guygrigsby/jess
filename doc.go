// Package jess is a streaming agent-loop runtime for Go applications.
//
// The harness owns the iterate-until-no-more-tool-calls loop and the
// event stream back to the caller. It does not own transport, config,
// or credentials — the host wires those in by implementing the
// Provider, ToolRunner, and store interfaces.
//
// jess was extracted from the talon gateway (github.com/guygrigsby/talon)
// after the loop had proven itself there. The clean-slate package design
// is intentional: the talon-internal version had grown coupling to
// merged-config readers and OS path layouts that don't belong in a
// reusable library.
//
// Status: pre-1.0. Expect API churn until the runtime has at least one
// independent caller beyond talon.
package jess
