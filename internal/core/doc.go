// Package core is the business logic layer of the GenAI service, sitting between the DAO layer
// (data access) and the handler layer (transport).
//
// It owns the generation lifecycle and the caps that admit a generation. A submit, a poll, a
// provider's notification and the scheduled sweep all move a generation forward through the same
// check, which calls the provider through a connector and records the outcome as a conditional write
// on the generation.
package core
