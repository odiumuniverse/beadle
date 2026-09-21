package bundle

// SetRenderSeamForTest installs a hook that mutates the rendered file set
// before the version is derived from it. The returned function restores the
// previous seam.
func SetRenderSeamForTest(seam func(Host, map[string][]byte)) func() {
	previous := renderSeam
	renderSeam = seam

	return func() { renderSeam = previous }
}
