package zfs

import (
	"context"
	"fmt"
)

// SystemStatus aggregates everything `pg zfs status` displays.
type SystemStatus struct {
	Distro       Distro
	DistroErr    error
	Health       Health
	CanPrivilege bool
	Root         bool
	Pools        []PoolRow
	PoolErr      error
	SecureBoot   bool
}

// LoadStatus probes the system without requiring privileges.
func LoadStatus(ctx context.Context, r *Runner) SystemStatus {
	status := SystemStatus{
		CanPrivilege: r.CanPrivilege(),
		Root:         r.Root(),
		SecureBoot:   SecureBootEnabled(),
	}
	distro, err := DetectDistro()
	status.Distro = distro
	status.DistroErr = err
	status.Health = ProbeHealth(ctx, r)
	if status.Health.Installed() {
		if status.CanPrivilege {
			status.Pools, status.PoolErr = ListPools(ctx, r)
		} else {
			status.PoolErr = fmt.Errorf("pool details require root or passwordless sudo")
		}
	}
	return status
}
