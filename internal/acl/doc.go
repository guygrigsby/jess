// Package acl is jess's anti-corruption layer: the single place that imports
// github.com/voocel/agentcore. It translates between jess's vendor-free domain
// types (message, event, tool) and agentcore's types, so no other jess package
// depends on the harness. A boundary test enforces that agentcore is imported
// only from here.
package acl
