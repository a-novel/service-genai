// Package dao is the data access layer of the GenAI service. It contains the PostgreSQL DAOs that
// persist generations, their payloads and attempts, and the Usage Records.
//
// Each statement lives in a sibling .sql file embedded at build time. A transition only applies to
// the state it expects, so a DAO reports a lost race as a sentinel error and leaves the row as the
// winning check wrote it.
package dao
