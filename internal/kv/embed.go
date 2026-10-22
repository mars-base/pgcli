package kv

import "embed"

//go:embed sql/*.sql
var SQLs embed.FS
