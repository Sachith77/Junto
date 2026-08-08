// Package migrations embeds the SQL migration files into the binary.
//
// Migrations ship *inside* the binary rather than as loose files next to it. This means a
// deployed artifact can always migrate itself to the schema version it was compiled against,
// with no "did the right .sql files get copied?" failure mode — which is the single most
// common way migration tooling goes wrong in production.
//
// We use golang-migrate as a library rather than its CLI: the CLI selects database drivers
// via build tags, which is awkward to reproduce reliably, and shelling out to a separate
// binary is one more thing that has to exist on the host.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
