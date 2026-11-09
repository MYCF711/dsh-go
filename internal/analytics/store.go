package analytics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// analyticsDir / policyFile / eventsPrefix / pendingFile 是本地管道的唯一布局。
const (
	analyticsDirName = "analytics"
	policyFileName   = "policy.json"
	eventsPrefix     = "events-"
	eventsSuffix     = ".jsonl"
	pendingFileName  = "pending-export.jsonl"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func analyticsDir(home string) string {
	return filepath.Join(home, "state", analyticsDirName)
}

func eventsFilePath(dir string, at time.Time) string {
	return filepath.Join(dir, eventsPrefix+at.UTC().Format("200601")+eventsSuffix)
}

// listEventFiles 返回按文件名（=月份）升序的事件文件。
func listEventFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, eventsPrefix) && strings.HasSuffix(name, eventsSuffix) {
			files = append(files, filepath.Join(dir, name))
		}
	}
	sort.Strings(files)
	return files, nil
}

// lastHashOfFiles 读最后一个非空行的 hash。空文件/没有文件 = 空串（链头）。
func lastHashOfFiles(files []string) (string, error) {
	for index := len(files) - 1; index >= 0; index-- {
		hash, ok, err := lastHashOfFile(files[index])
		if err != nil {
			return "", err
		}
		if ok {
			return hash, nil
		}
	}
	return "", nil
}

func lastHashOfFile(path string) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	const tailBytes = 64 << 10
	offset := info.Size() - tailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", false, err
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return "", false, fmt.Errorf("analytics: %s 最后一条记录损坏: %w", path, err)
		}
		if record.Hash == "" {
			return "", false, fmt.Errorf("analytics: %s 最后一条记录缺 hash", path)
		}
		return record.Hash, true, nil
	}
	return "", false, nil
}

// appendRecordLocked 在调用方持锁的情况下追加一行并更新 lastHash。
func appendRecordLocked(dir string, at time.Time, record Record, lastHash string) (Record, error) {
	record.PrevHash = lastHash
	hash, err := hashRecord(record)
	if err != nil {
		return Record{}, err
	}
	record.Hash = hash
	line, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	path := eventsFilePath(dir, at)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Record{}, err
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		file.Close()
		return Record{}, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return Record{}, err
	}
	if err := file.Close(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// readAllRecords 按文件顺序读取所有记录；用于 Dump/Verify/Aggregate。
func readAllRecords(dir string) ([]Record, error) {
	files, err := listEventFiles(dir)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(file)
		for {
			var record Record
			if err := decoder.Decode(&record); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				file.Close()
				return nil, fmt.Errorf("analytics: 读取 %s 失败: %w", path, err)
			}
			records = append(records, record)
		}
		file.Close()
	}
	return records, nil
}

// verifyChain 复算整条链；返回记录数。
func verifyChain(dir string) (int, error) {
	records, err := readAllRecords(dir)
	if err != nil {
		return 0, err
	}
	previous := ""
	for index, record := range records {
		if record.PrevHash != previous {
			return index, fmt.Errorf("analytics: 第 %d 条记录 prevHash 断裂（期望 %q，实际 %q）", index, previous, record.PrevHash)
		}
		expected, err := hashRecord(record)
		if err != nil {
			return index, err
		}
		if record.Hash != expected {
			return index, fmt.Errorf("analytics: 第 %d 条记录 hash 不匹配（期望 %s，实际 %s）", index, expected, record.Hash)
		}
		if record.PolicyVersion <= 0 {
			return index, fmt.Errorf("analytics: 第 %d 条记录 policyVersion 非法", index)
		}
		previous = record.Hash
	}
	return len(records), nil
}

// countRecords 与 readAllRecords 同源，只计数（Purge 用）。
func countRecords(dir string) (int, error) {
	records, err := readAllRecords(dir)
	if err != nil {
		return 0, err
	}
	return len(records), nil
}
