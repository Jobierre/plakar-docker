//go:build !windows
// +build !windows

/*
 * Copyright (c) 2023 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package fs

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PlakarKorp/plakar/objects"
	"github.com/PlakarKorp/plakar/snapshot/importer"
	"github.com/pkg/xattr"
)

type namecache struct {
	uidToName map[uint64]string
	gidToName map[uint64]string

	mu sync.RWMutex
}

// Worker pool to handle file scanning in parallel
func walkDir_worker(jobs <-chan string, results chan<- *importer.ScanResult, wg *sync.WaitGroup, namecache *namecache) {
	defer wg.Done()

	for path := range jobs {
		info, err := os.Lstat(path)
		if err != nil {
			results <- importer.NewScanError(path, err)
			continue
		}

		extendedAttributes, err := xattr.List(path)
		if err != nil {
			results <- importer.NewScanError(path, err)
			continue
		}

		fileinfo := objects.FileInfoFromStat(info)

		namecache.mu.RLock()
		if uname, ok := namecache.uidToName[fileinfo.Uid()]; !ok {
			if u, err := user.LookupId(fmt.Sprintf("%d", fileinfo.Uid())); err == nil {
				fileinfo.Lusername = u.Username

				namecache.mu.RUnlock()
				namecache.mu.Lock()
				namecache.uidToName[fileinfo.Uid()] = u.Username
				namecache.mu.Unlock()
				namecache.mu.RLock()
			}
		} else {
			fileinfo.Lusername = uname
		}

		if gname, ok := namecache.gidToName[fileinfo.Uid()]; !ok {
			if g, err := user.LookupGroupId(fmt.Sprintf("%d", fileinfo.Gid())); err == nil {
				fileinfo.Lgroupname = g.Name

				namecache.mu.RUnlock()
				namecache.mu.Lock()
				namecache.gidToName[fileinfo.Gid()] = g.Name
				namecache.mu.Unlock()
				namecache.mu.RLock()
			}
		} else {
			fileinfo.Lgroupname = gname
		}
		namecache.mu.RUnlock()

		var originFile string
		if fileinfo.Mode()&os.ModeSymlink != 0 {
			originFile, err = os.Readlink(path)
			if err != nil {
				results <- importer.NewScanError(path, err)
				continue
			}
		}
		results <- importer.NewScanRecord(filepath.ToSlash(path), originFile, fileinfo, extendedAttributes)
		for _, attr := range extendedAttributes {
			bytes, err := xattr.Get(path, attr)
			if err != nil {
				results <- importer.NewScanError(path, err)
				continue
			}

			fileinfo := objects.FileInfo{
				Lname: filepath.Base(path) + ":" + attr,
				Lsize: int64(len(bytes)),
				Lmode: 0,
			}

			results <- importer.NewScanXattr(filepath.ToSlash(path), attr, fileinfo)
		}
	}
}

func walkDir_addPrefixDirectories(rootDir string, jobs chan<- string, results chan<- *importer.ScanResult) {
	// Clean the directory and split the path into components
	directory := filepath.Clean(rootDir)
	atoms := strings.Split(directory, string(os.PathSeparator))

	for i := 0; i < len(atoms)-1; i++ {
		path := filepath.Join(atoms[0 : i+1]...)

		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}

		if _, err := os.Stat(path); err != nil {
			results <- importer.NewScanError(path, err)
			continue
		}

		jobs <- path
	}
}

func walkDir_walker(rootDir string, numWorkers int) (<-chan *importer.ScanResult, error) {
	results := make(chan *importer.ScanResult, 1000) // Larger buffer for results
	jobs := make(chan string, 1000)                  // Buffered channel to feed paths to workers
	namecache := &namecache{
		uidToName: make(map[uint64]string),
		gidToName: make(map[uint64]string),
	}

	var wg sync.WaitGroup

	// Launch worker pool
	for w := 1; w <= numWorkers; w++ {
		wg.Add(1)
		go walkDir_worker(jobs, results, &wg, namecache)
	}

	// Start walking the directory and sending file paths to workers
	go func() {
		defer close(jobs)

		info, err := os.Lstat(rootDir)
		if err != nil {
			results <- importer.NewScanError(rootDir, err)
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			originFile, err := os.Readlink(rootDir)
			if err != nil {
				results <- importer.NewScanError(rootDir, err)
				return
			}

			if !filepath.IsAbs(originFile) {
				originFile = filepath.Join(filepath.Dir(rootDir), originFile)
			}
			jobs <- rootDir
			rootDir = originFile
		}

		// Add prefix directories first
		walkDir_addPrefixDirectories(rootDir, jobs, results)

		err = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				results <- importer.NewScanError(path, err)
				return nil
			}
			jobs <- path
			return nil
		})
		if err != nil {
			results <- importer.NewScanError(rootDir, err)
		}
	}()

	// Close the results channel when all workers are done
	go func() {
		wg.Wait()
		close(results)
	}()

	return results, nil
}
