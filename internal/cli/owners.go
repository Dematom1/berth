package cli

import (
	"os"
	"strconv"

	configpkg "github.com/Dematom1/dibs/internal/config"
	ownerpkg "github.com/Dematom1/dibs/internal/owner"
)

func newOwners() ownerpkg.Registry {
	owners := []ownerpkg.Definition{ownerpkg.PID(), ownerpkg.Session(), ownerpkg.Heartbeat()}
	if prOwnerEnabled() {
		owners = append(owners, ownerpkg.PR())
	}
	return ownerpkg.NewRegistry(owners...)
}

func prOwnerEnabled() bool { return optedIn("DIBS_PR_OWNER", configpkg.PROwner) }

// liveOwnerProtectionOff names the settings that allow --protect-live-owner.
const liveOwnerProtectionOff = `--protect-live-owner is off; set DIBS_LIVE_OWNER_PROTECTION=1 or "live_owner_protection": true in the user config`

func liveOwnerProtectionEnabled() bool {
	return optedIn("DIBS_LIVE_OWNER_PROTECTION", configpkg.LiveOwnerProtection)
}

// consumerContractOff names the settings that turn on the versioned consumer contract.
const consumerContractOff = `the consumer contract is off; set DIBS_CONSUMER_CONTRACT=1 or "consumer_contract": true in the user config`

func consumerContractEnabled() bool {
	return optedIn("DIBS_CONSUMER_CONTRACT", configpkg.ConsumerContract)
}

// optedIn lets the DIBS_* variable override the user config either way; it has no BERTH_* alias.
func optedIn(env string, config func() bool) bool {
	if value, ok := os.LookupEnv(env); ok {
		enabled, err := strconv.ParseBool(value)
		return err == nil && enabled
	}
	return config()
}
