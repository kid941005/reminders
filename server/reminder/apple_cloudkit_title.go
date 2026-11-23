package reminder

import "smallgo/server/reminder/cloudkit"

// Thin wrappers keep the historical package-local names used by the CloudKit
// client/service and their tests, while the CRDT codec lives in cloudkit.
func ckEncodeTitle(title string) (string, error) { return cloudkit.EncodeTitle(title) }
func ckExtractTitle(tdB64 string) string         { return cloudkit.ExtractTitle(tdB64) }
func ckNewUUIDString() string                    { return cloudkit.NewUUIDString() }
