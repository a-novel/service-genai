// Package core is the business logic layer of the GenAI service, sitting between the DAO layer
// (data access) and the handler layer (transport).
//
// It owns the generation lifecycle and the caps that admit a generation. A submit sends the
// generation's provider call itself. A poll and the scheduled sweep then move it forward through the
// same check, which calls the provider through a connector and settles the generation with a
// conditional write.
package core
