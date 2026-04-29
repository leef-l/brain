package scripts

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

func ListFiles2() {
    root := `C:\Users\Public\project\brain\sdk\docs`
    filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
        if err != nil { return nil }
        if !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
            // Print filename only
            fmt.Println(info.Name())
        }
        return nil
    })
}
