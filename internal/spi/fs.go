package spi

// FS 是文件系统接缝。所有工具都经它读写，这样两条策略只实现一次：
//
//  1. **路径归一**：一律正斜杠、相对工作区根，越界直接拒绝；
//  2. **读后写**（read-before-write）：文件存在时，必须先读过才能改；
//     读过之后文件又被别人改过，必须重新读——不允许盲覆盖。
//
// 工具自己不要实现这两条（AGENTS §3.7）。
type FS interface {
	// Root 返回工作区根（正斜杠绝对路径）。
	Root() string
	// Resolve 把调用方给的路径归一成工作区内的绝对路径；越界返回错误。
	Resolve(path string) (string, error)
	// Read 读文件，并登记「已观测」；登记发生在读之前，宁可多要求一次重读。
	Read(path string) ([]byte, error)
	// CheckWritable 在写之前确认读后写策略。文件不存在时允许创建。
	CheckWritable(path string) error
	// Stat 查文件是否存在与大小（不改观测状态）。
	Stat(path string) (size int64, exists bool, err error)
	// Write 写文件（自动建父目录），成功后把新内容登记为已观测。
	Write(path string, data []byte) error
}
