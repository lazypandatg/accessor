package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/fatih/structtag"
	"go/ast"
	"io/ioutil"

	"go/printer"
	"go/token"
	"go/types"
	"golang.org/x/tools/go/packages"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

const AccessRead = "r"
const AccessWrite = "w"
const AccessTagName = "access"

var (
	typeNames = flag.String("type", "", "comma-separated list of type names; must be set")
	output    = flag.String("output", "", "output file name; default srcdir/<type>_accessor.go")
)

// Usage is a replacement usage function for the flags package.
func Usage() {
	fmt.Fprintf(os.Stderr, "Usage of accessor:\n")
	fmt.Fprintf(os.Stderr, "\taccessor [flags] -type T [directory]\n")
	fmt.Fprintf(os.Stderr, "\taccessor [flags] -type T files... # Must be a single package\n")
	fmt.Fprintf(os.Stderr, "For more information, see:\n")
	fmt.Fprintf(os.Stderr, "\thttps://gitee.com/dwdcth/accessor.git\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	flag.PrintDefaults()
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("accessor: ")
	flag.Usage = Usage
	flag.Parse()
	if len(*typeNames) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	types := strings.Split(*typeNames, ",")

	// We accept either one directory or a list of files. Which do we have?
	args := flag.Args()
	if len(args) == 0 {
		// Default: process whole package in current directory.
		args = []string{"."}
	}

	// Parse the package once.
	var dir string
	g := Generator{
		buf: make(map[string]*bytes.Buffer),
		//structInfo: make(map[string]StructFieldInfoArr), //一定不能初始化
		walkMark: make(map[string]bool),
	}
	if len(args) == 1 && isDirectory(args[0]) {
		dir = args[0]
	} else {
		dir = filepath.Dir(args[0])
	}

	//ParseStruct(dir, nil, "access")
	g.parsePackage(args)

	// Print the header and package clause.
	// Run generate for each type.
	for i, typeName := range types {
		g.generate(typeName)
		// AccessWrite to file.
		outputName := *output
		if outputName == "" {
			baseName := fmt.Sprintf("%s_accessor.go", types[i])
			outputName = filepath.Join(dir, strings.ToLower(baseName))
		}
		buf := g.buf[typeName]
		var src = (buf).Bytes()
		err := ioutil.WriteFile(outputName, src, 0644)
		if err != nil {
			log.Fatalf("writing output: %s", err)
		}
	}

}

// isDirectory reports whether the named file is a directory.
func isDirectory(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		log.Fatal(err)
	}
	return info.IsDir()
}

// Generator holds the state of the analysis. Primarily used to buffer
// the output for format.Source.
type Generator struct {
	buf        map[string]*bytes.Buffer // Accumulated output.
	pkg        *Package                 // Package we are scanning.
	structInfo map[string]StructFieldInfoArr
	walkMark   map[string]bool
}

func (g *Generator) Printf(structName, format string, args ...interface{}) {
	buf, ok := g.buf[structName]
	if !ok {
		buf = bytes.NewBufferString("")
		g.buf[structName] = buf
	}
	fmt.Fprintf(buf, format, args...)
}

// File holds a single parsed file and associated data.
type File struct {
	pkg     *Package  // Package to which this file belongs.
	file    *ast.File // Parsed AST.
	fileSet *token.FileSet
	// These fields are reset for each type being generated.
	typeName string // Name of the constant type.

}

type Package struct {
	name  string
	defs  map[*ast.Ident]types.Object
	files []*File
}

// parsePackage analyzes the single package constructed from the patterns and tags.
// parsePackage exits if there is an error.
func (g *Generator) parsePackage(patterns []string) {
	cfg := &packages.Config{
		Mode:  packages.LoadSyntax,
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		log.Fatal(err)
	}
	if len(pkgs) != 1 {
		log.Fatalf("error: %d packages found", len(pkgs))
	}
	g.addPackage(pkgs[0])
}

// addPackage adds a type checked Package and its syntax files to the generator.
func (g *Generator) addPackage(pkg *packages.Package) {
	g.pkg = &Package{
		name:  pkg.Name,
		defs:  pkg.TypesInfo.Defs,
		files: make([]*File, len(pkg.Syntax)),
	}

	for i, file := range pkg.Syntax {
		g.pkg.files[i] = &File{
			file:    file,
			pkg:     g.pkg,
			fileSet: pkg.Fset,
		}
	}
}

// generate produces the String method for the named type.
func (g *Generator) generate(typeName string) {
	for _, file := range g.pkg.files { //按包来的，读取包下的所有文件
		// Set the state for this run of the walker.
		file.typeName = typeName
		//ast.Print(file.fileSet, file.file)
		if file.file != nil {

			structInfo, err := ParseStruct(file.file, file.fileSet, AccessTagName)
			if err != nil {
				fmt.Println("失败:" + err.Error())
				return
			}

			for stName, info := range structInfo {
				if stName != typeName {
					continue
				}
				g.Printf(stName, "// Code generated by \"accessor %s\"; DO NOT EDIT.\n", strings.Join(os.Args[1:], " "))
				g.Printf(stName, "\n")
				g.Printf(stName, "package %s\n", g.pkg.name)
				g.Printf(stName, "\n")
				for _, field := range info {
					for _, access := range field.Access {
						switch access {
						case AccessWrite:
							g.Printf(stName, "%s\n", genSetter(stName, field.Name, field.Type))
						case AccessRead:
							g.Printf(stName, "%s\n", genGetter(stName, field.Name, field.Type))
						}
					}

				}
			}

		}
	}

}

type StructFieldInfo struct {
	Name   string
	Type   string
	Access []string
}
type StructFieldInfoArr = []StructFieldInfo

func ParseStruct(file *ast.File, fileSet *token.FileSet, tagName string) (structMap map[string]StructFieldInfoArr, err error) {
	structMap = make(map[string]StructFieldInfoArr)

	collectStructs := func(x ast.Node) bool {
		ts, ok := x.(*ast.TypeSpec)
		if !ok || ts.Type == nil {
			return true
		}

		// 获取结构体名称
		structName := ts.Name.Name

		s, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		fileInfos := make([]StructFieldInfo, 0)
		for _, field := range s.Fields.List {
			log.Println(field)
			if len(field.Names) == 0 {
				continue
			}
			name := field.Names[0].Name
			info := StructFieldInfo{Name: name}
			var typeNameBuf bytes.Buffer
			err := printer.Fprint(&typeNameBuf, fileSet, field.Type)
			if err != nil {
				fmt.Println("获取类型失败:", err)
				return true
			}

			info.Type = typeNameBuf.String()
			if field.Tag != nil { // 有tag
				tag := field.Tag.Value
				tag = strings.Trim(tag, "`")
				tags, err := structtag.Parse(tag)
				if err != nil {
					return true
				}
				access, err := tags.Get(tagName)
				if err == nil {
					access.Options = append(access.Options, access.Name)
					for i, v := range access.Options {
						if v == AccessRead || v == AccessWrite {
							continue
						}
						access.Options = append(access.Options[:i], access.Options[i+1:]...)
					}
				}
				info.Access = access.Options
			} else {
				firstChar := name[0:1]
				if strings.ToUpper(firstChar) == firstChar { //大写
					info.Access = []string{AccessRead, AccessWrite}
				} else { // 小写
					info.Access = []string{AccessRead}
				}
			}
			fileInfos = append(fileInfos, info)
		}
		structMap[structName] = fileInfos
		return false
	}

	ast.Inspect(file, collectStructs)

	return structMap, nil
}

func genSetter(structName, fieldName, typeName string) string {
	tpl := `func ({{.Receiver}} *{{.Struct}}) Set{{.Field}}(param {{.Type}}) {
	{{.Receiver}}.{{.Field}} = param
}`
	t := template.New("setter")
	t = template.Must(t.Parse(tpl))
	res := bytes.NewBufferString("")
	t.Execute(res, map[string]string{
		"Receiver": strings.ToLower(structName[0:1]),
		"Struct":   structName,
		"Field":    fieldName,
		"Type":     typeName,
	})
	return res.String()
}

func genGetter(structName, fieldName, typeName string) string {
	tpl := `func ({{.Receiver}} *{{.Struct}}) Get{{.Field}}() {{.Type}} {
	return {{.Receiver}}.{{.Field}}
}`
	t := template.New("getter")
	t = template.Must(t.Parse(tpl))
	res := bytes.NewBufferString("")
	t.Execute(res, map[string]string{
		"Receiver": strings.ToLower(structName[0:1]),
		"Struct":   structName,
		"Field":    fieldName,
		"Type":     typeName,
	})
	return res.String()
}
