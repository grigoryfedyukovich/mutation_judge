package incremental

func IsIdentifierStart(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z'
}
