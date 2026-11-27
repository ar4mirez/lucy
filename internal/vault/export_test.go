package vault

// Tests derive keys with a light PBKDF2 work factor, so the suite stays fast; the format and the
// checks are the same.
func init() {
	kdfIterations = 1000
	minIterations = 1000
}
