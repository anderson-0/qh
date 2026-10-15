package secret

// publishKeyFile publishes a completed file without replacing an existing key.
// Keep hard links as the fast path; only unsupported links justify trying a
// native exclusive rename. Neither path may expose a partially written key.
func publishKeyFile(temp, path string, link, renameNoReplace func(string, string) error) error {
	if err := link(temp, path); err != nil {
		if !unsupportedKeyFileLink(err) {
			return err
		}
		return renameNoReplace(temp, path)
	}
	return nil
}
