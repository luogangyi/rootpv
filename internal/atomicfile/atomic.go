/*
 * Copyright (c) 2026 OpenFuyao Contributors.
 * RootPV is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 * http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

// Package atomicfile 提供带文件和目录同步语义的原子文件替换操作。
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Write 将数据先写入 tmp，再原子替换 path，并同步目标目录。
// rename 成功后若目录同步失败，path 已经可见为新内容，但崩溃后的目录持久化状态未知；
// 函数会返回该错误，供调用方按未完全持久化处理。
func Write(tmp, path string, data []byte, perm os.FileMode) (returnErr error) {
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open temporary file: %w", err)
	}
	defer func() {
		if file != nil {
			if err := file.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close temporary file: %w", err))
			}
		}
		if returnErr != nil {
			if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary file: %w", err))
			}
		}
	}()

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		file = nil
		return fmt.Errorf("close temporary file: %w", err)
	}
	file = nil
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace target file: %w", err)
	}
	if err := syncParent(path); err != nil {
		return fmt.Errorf("sync target directory: %w", err)
	}
	return nil
}

func syncParent(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
