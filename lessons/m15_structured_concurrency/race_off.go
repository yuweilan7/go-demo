//go:build !race

package lesson

// 普通入口编译原Pool child也用普通构建，故障关系不依赖检测器时延。
const legacyRace = false
