package events

// Secret stores: a value could not be read (a process, task or deployment
// did not start), an unreachable store's last known value was used
// instead, or a site was recycled because a secret it uses changed.
const (
	SecretFailed  = "secret.failed"
	SecretStale   = "secret.stale"
	SecretRotated = "secret.rotated"
)

func init() { AllTypes = append(AllTypes, SecretFailed, SecretStale, SecretRotated) }
