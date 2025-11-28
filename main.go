package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

/*
	[Chapter 4: The Staging Area (Index)]

	Gogit 에서는 .gogit/index 라는 바이너리 파일로 관리됩니다.

	[Binary Format Specification for GoGit Index]
	----------------------------------------------------------------
	| Header (12 bytes) |
	|   - Signature: "DIRC" (4 bytes)
	|   - Version:   1      (4 bytes, Big Endian)
	|   - Count:     N      (4 bytes, Big Endian) number of entries
	----------------------------------------------------------------
	| Entry 1 (Variable Length)                                    |
	|   - Mode:      4 bytes (Big Endian)                          |
	|   - SHA-1:     20 bytes                                      |
	|   - PathLen:   2 bytes (Big Endian)                          |
	|   - Path:      PathLen bytes                                 |
	----------------------------------------------------------------
	| Entry 2 ...                                                  |
	----------------------------------------------------------------
*/

// header 를 제외한 컨텐츠를 구분하기 위해서는 구분자가 필요함
const NUL = "\000"

type IndexEntry struct {
	Mode    uint32
	Hash    [20]byte
	PathLen uint16
	Path    string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: gogit <command> [args...]")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		cmdInit()
		fmt.Println("Initializing repository...")
		os.Exit(0)
	case "hash-object":
		if len(os.Args) < 3 {
			fmt.Println("Usage: gogit hash-object <filename>")
			os.Exit(1)
		}
		hash, err := hashObject(os.Args[2], "blob")
		if err != nil {
			fmt.Printf("Error hashing object: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(hash)
		os.Exit(0)
	case "add":
		if len(os.Args) < 3 {
			fmt.Println("Usage: gogit add <filename>")
			os.Exit(1)
		}
		err := cmdAdd(os.Args[2])
		if err != nil {
			fmt.Printf("Error adding file: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Added file:", os.Args[2])
		os.Exit(0)
	case "ls-files":
		err := cmdLsFile()
		if err != nil {
			fmt.Printf("Error listing files: %v\n", err)
			os.Exit(1)
		}
	case "write-tree":
		hash, err := cmdWriteTree(".")
		if err != nil {
			fmt.Printf("Error writing tree: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(hash)
		os.Exit(0)
	case "commit-tree":
		// Usage: gogit commit-tree <tree_sha> -p <parent_sha> -m <message>
		if len(os.Args) < 4 {
			fmt.Println("Usage: gogit commit-tree <tree_sha> -m <message> [-p <parent_sha>]")
			os.Exit(1)
		}
		cmdCommitTree(os.Args[2], os.Args[3:])
	case "log":
		// Usage: gogit log <commit_sha>
		if len(os.Args) < 3 {
			fmt.Println("Usage: gogit log <commit_sha>")
			os.Exit(1)
		}
		cmdLog(os.Args[2])
	case "ls-tree":
		if len(os.Args) < 3 {
			fmt.Println("Usage: gogit ls-tree <tree-id>")
			os.Exit(1)
		}
		cmdLsTree(os.Args[2])
		fmt.Println("Listing tree...")
		os.Exit(0)
	case "cat-file":
		if len(os.Args) < 4 || os.Args[2] != "-p" {
			fmt.Println("Usage: gogit cat-file [-p] <object-id>")
			os.Exit(1)
		}
		fmt.Printf("Object ID: %s\n", os.Args[3])
		cmdCatFile(os.Args[3])
		fmt.Println("Displaying file...")
		os.Exit(0)
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func cmdLsFile() error {
	entries, err := readIndex()
	if err != nil {
		return err
	}

	for _, entry := range entries {
		fmt.Printf("%s\n", entry.Path)
	}
	return nil
}

func cmdAdd(path string) error {
	hashStr, err := hashObject(path, "blob")
	if err != nil {
		return err
	}

	// 40자 hex 문자열을 20바이트 []byte 슬라이스로 변환
	hashBytes, _ := hex.DecodeString(hashStr)
	var hashArr [20]byte
	copy(hashArr[:], hashBytes)

	entries, err := readIndex()
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	relPath := path
	found := false
	for i, entry := range entries {
		if entry.Path == relPath {
			entries[i].Hash = hashArr
			entries[i].Mode = 0100644
			found = true
			break
		}
	}

	if !found {
		entries = append(entries, IndexEntry{
			Mode: 0100644,
			Hash: hashArr,
			Path: relPath,
		})
	}

	return writeIndex(entries)
}

func readIndex() ([]IndexEntry, error) {
	indexPath := ".gogit/index"
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var sig [4]byte
	if _, err := f.Read(sig[:]); err != nil {
		return nil, err
	}

	if string(sig[:]) != "DIRC" {
		return nil, fmt.Errorf("invalid index signature")
	}

	var version, count uint32
	binary.Read(f, binary.BigEndian, &version)
	binary.Read(f, binary.BigEndian, &count)

	entries := make([]IndexEntry, count)
	for i := range entries {
		var mode uint32
		if err := binary.Read(f, binary.BigEndian, &mode); err != nil {
			return nil, err
		}
		entries[i].Mode = mode

		var hash [20]byte
		if err := binary.Read(f, binary.BigEndian, &hash); err != nil {
			return nil, err
		}
		entries[i].Hash = hash

		var pathLen uint16
		if err := binary.Read(f, binary.BigEndian, &pathLen); err != nil {
			return nil, err
		}

		path := make([]byte, pathLen)
		if _, err := io.ReadFull(f, path); err != nil {
			return nil, err
		}
		entries[i].Path = string(path)
	}

	return entries, nil
}

func writeIndex(entries []IndexEntry) error {
	indexPath := ".gogit/index"
	f, err := os.Create(indexPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.WriteString("DIRC"); err != nil {
		return err
	}

	binary.Write(f, binary.BigEndian, uint32(1))
	binary.Write(f, binary.BigEndian, uint32(len(entries)))

	for _, entry := range entries {
		binary.Write(f, binary.BigEndian, entry.Mode)
		f.Write(entry.Hash[:])
		binary.Write(f, binary.BigEndian, uint16(len(entry.Path)))
		f.WriteString(entry.Path)
	}

	return nil
}

// Init: 저장소 초기화
func cmdInit() {
	dirs := []string{".gogit", ".gogit/objects", ".gogit/refs"}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Printf("Error creating directory %s: %v\n", dir, err)
			os.Exit(1)
		}
	}

	headFile := ".gogit/HEAD"
	if _, err := os.Stat(headFile); os.IsNotExist(err) {
		os.WriteFile(headFile, []byte("ref: refs/heads/master\n"), 0644)
	}
	fmt.Println("Initialized emtpy goGit repository in .gogit")
}

func hashObject(path string, typeStr string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Error reading file %s: %v", path, err)
	}
	return storeObject(typeStr, content)
}

// typeStr: "blob" 또는 "tree"
func storeObject(typeStr string, content []byte) (string, error) {
	header := fmt.Sprintf("%s %d%s", typeStr, len(content), NUL)
	store := append([]byte(header), content...)

	// Checksum 계산 (SHA-1 Hashing)
	// Hash 함수기 때문에 content 가 바뀌지 않는다면 동일한 해시값이 생성됨.
	hasher := sha1.New()
	hasher.Write(store)
	hashBytes := hasher.Sum(nil)
	hashString := hex.EncodeToString(hashBytes)
	fmt.Printf("Hash: %s\n", hashString)

	// 저장
	// 해시값을 이용하여 경로를 생성하고, 내용은 zlib 으로 압축하여 저장
	if err := saveObject(hashString, store); err != nil {
		fmt.Printf("Error saving object %s: %v\n", hashString, err)
		os.Exit(1)
	}

	return hashString, nil
}

func saveObject(hash string, content []byte) error {
	// 2글자로 하는 이유는 적당하게 디렉토리를 생성하기 위해서 hash 당 dir 이 생기면 너무 많아지기 때문
	dirName := hash[:2]
	fileName := hash[2:]
	path := filepath.Join(".gogit", "objects", dirName)

	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}

	fullPath := filepath.Join(path, fileName)

	// 이미 존재하는 객체라면 덮어쓰지 않아도 됨
	if _, err := os.Stat(fullPath); err == nil {
		fmt.Printf("Object %s already exists\n", fullPath)
		return nil
	}

	f, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zlib.NewWriter(f)
	if _, err := zw.Write(content); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	return nil
}

func cmdWriteTree(dirPath string) (string, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return "", err
	}

	var buffer bytes.Buffer

	// Git 은 entries 를 정렬하고 저장합니다.
	// 여기서는 순차적으로 쓰인다는 가정하에 생략하고 진행하겠습니다
	for _, entry := range entries {
		name := entry.Name()

		if name == ".gogit" || name == ".git" || name == ".gitignore" {
			continue
		}

		path := filepath.Join(dirPath, name)
		var mode string
		var sha string

		if entry.IsDir() {
			mode = "40000" // Directory mode
			sha, err = cmdWriteTree(path)
			if err != nil {
				return "", err
			}
		} else {
			mode = "100644" // File mode
			sha, err = hashObject(path, "blob")
			if err != nil {
				return "", err
			}
		}

		// Tree Entry 포맷: [mode] [name]\0[SHA-1 Binary 20bytes]
		shaBytes, err := hex.DecodeString(sha)
		if err != nil {
			return "", err
		}

		fmt.Printf("%s %s\x00", mode, name)

		fmt.Fprintf(&buffer, "%s %s\x00", mode, name)
		buffer.Write(shaBytes)
	}

	return storeObject("tree", buffer.Bytes())
}

func cmdCommitTree(treeSha string, args []string) {
	parentSha := ""
	message := ""

	for i := 0; i < len(args); i++ {
		if args[i] == "-p" && i+1 < len(args) {
			parentSha = args[i+1]
			i++
		} else if args[i] == "-m" && i+1 < len(args) {
			message = args[i+1]
			i++
		}
	}

	if message == "" {
		fmt.Println("Error: commit message is required")
		return
	}

	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "tree %s\n", treeSha)
	if parentSha != "" {
		fmt.Fprintf(&buffer, "parent %s\n", parentSha)
	}

	timestamp := time.Now().Unix()
	timezone := "+0000"
	author := fmt.Sprintf("GoGit User <user@example.com> %d %s", timestamp, timezone)

	fmt.Fprintf(&buffer, "author %s\n", author)
	fmt.Fprintf(&buffer, "committer %s\n", author)
	fmt.Fprintf(&buffer, "\n%s\n", message)

	sha, err := storeObject("commit", buffer.Bytes())
	if err != nil {
		fmt.Printf("Error committing tree: %v\n", err)
		return
	}
	fmt.Println(sha)
}

func cmdLog(commitSha string) {
	currentSha := commitSha

	for {
		content, err := readObject(currentSha)
		if err != nil {
			fmt.Printf("Error reading commit %s: %v\n", currentSha, err)
			break
		}

		nullIndex := bytes.IndexByte(content, 0)
		payload := string(content[nullIndex+1:])
		lines := strings.Split(payload, "\n")

		fmt.Printf("commit %s\n", currentSha)

		parentSha := ""

		// tree 1231231231
		// parent 12312321323
		// author GoGit User <user@example.com> 12312312 KST
		// committer GoGit User <user@example.com> 12312312 KST
		// message
		for _, line := range lines {
			if strings.HasPrefix(line, "parent ") {
				parentSha = strings.TrimPrefix(line, "parent ")
			} else if strings.HasPrefix(line, "author ") {
				fmt.Printf("author %s\n", line)
			} else if strings.HasPrefix(line, "committer ") {
				fmt.Printf("committer %s\n", line)
			} else if line == "" {
				break
			}
		}

		msgStartIndex := -1
		for i, line := range lines {
			if line == "" {
				msgStartIndex = i + 1
				break
			}
		}

		if msgStartIndex != -1 && msgStartIndex < len(lines) {
			fmt.Printf("\n    %s\n\n", strings.Join(lines[msgStartIndex:], "\n   "))
		}

		if parentSha == "" {
			break
		}
		currentSha = parentSha
	}
}

func cmdLsTree(hash string) {
	content, err := readObject(hash)
	if err != nil {
		fmt.Printf("Error reading object: %v\n", err)
		return
	}

	nullIndex := bytes.IndexByte(content, 0)
	if nullIndex == -1 {
		fmt.Println("No null byte found")
		return
	}

	payload := content[nullIndex+1:]

	buf := bytes.NewBuffer(payload)
	for buf.Len() > 0 {
		// 모드랑 이름 읽기 0 전까지
		line, err := buf.ReadBytes(0)
		if err != nil {
			fmt.Printf("Error reading line: %v\n", err)
			return
		}

		// "100644 filename\0" -> "100644 filename"
		lineStr := string(line[:len(line)-1])
		parts := strings.Split(lineStr, " ")
		mode := parts[0]
		name := parts[1]

		shaBytes := make([]byte, 20)
		buf.Read(shaBytes)
		shaStr := hex.EncodeToString(shaBytes)

		typeStr := "blob"
		if mode == "40000" || mode == "040000" {
			typeStr = "tree"
		}

		fmt.Printf("%s %s %s\t%s\n", mode, typeStr, shaStr, name)
	}
}

func readObject(hash string) ([]byte, error) {
	dirName := hash[:2]
	fileName := hash[2:]
	path := filepath.Join(".gogit", "objects", dirName, fileName)

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	zr, err := zlib.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	content, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}

	return content, nil
}

// 검증 및 디버깅용
func cmdCatFile(hash string) {
	dirName := hash[:2]
	fileName := hash[2:]
	path := filepath.Join(".gogit", "objects", dirName, fileName)

	f, err := os.Open(path)
	if err != nil {
		fmt.Printf("Error opening object: %v\n", err)
		return
	}
	defer f.Close()

	zr, err := zlib.NewReader(f)
	if err != nil {
		fmt.Printf("Error creating zlib reader: %v\n", err)
		return
	}
	defer zr.Close()

	content, err := io.ReadAll(zr)
	if err != nil {
		fmt.Printf("Error reading object: %v\n", err)
		return
	}

	fmt.Printf("%s\n", content)

	// 헤더 파싱
	nullIndex := -1
	for i, b := range content {
		if b == 0 {
			nullIndex = i
			break
		}
	}

	if nullIndex == -1 {
		fmt.Println("Invalid object format")
		return
	}

	header := content[:nullIndex]
	fmt.Printf("Header: %s\n", header)

	// 페이로드 파싱
	payload := content[nullIndex+1:]
	fmt.Printf("Payload: %s\n", payload)

	// 헤더와 페이로드를 분리하여 출력
	fmt.Printf("Header: %s\n", header)
	fmt.Printf("Payload: %s\n", payload)
}
