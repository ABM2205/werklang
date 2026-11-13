package scanner

// StartLineFallback converts a 1-indexed Pkl line number into a 0-indexed .werk line number.
func (c *ConfigBlock) StartLineFallback(pklOneBasedLine int) int {
	if pklOneBasedLine > 0 {
		return pklOneBasedLine - 1
	}
	if c != nil {
		return c.KeywordSpan.Start.Line
	}
	return 0
}
