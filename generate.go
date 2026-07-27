package servicegenai

// Generate proto stubs first (mockery depends on compiled packages).
//go:generate rm -rf internal/handlers/protogen
//go:generate go tool -modfile=buf.mod buf generate

// Generate mocks.
//go:generate go tool -modfile=mockery.mod mockery
