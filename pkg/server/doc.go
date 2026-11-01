// Package server resolves the server a claim references and enforces the
// server's allowedNamespaces (ADR 0002, "Server tiers"). Resolve is the only
// place in an operator that reads a server or its admin Secret.
package server
