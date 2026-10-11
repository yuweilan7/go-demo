//go:build race

package lesson

// Go自动在-race构建时提供race标签，原Pool child必须同步启用检测。
const legacyRace = true
