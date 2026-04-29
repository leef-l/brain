package scripts

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

func ListDocsFiles() {
    root := `C:\Users\Public\project\brain\docs`
    out, _ := os.Create(`C:\Users\Public\project\brain\docs_files.txt`)
    defer out.Close()
    filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
        if err != nil { return nil }
        if !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
            fmt.Fprintln(out, path)
        }
        return nil
    })
}
