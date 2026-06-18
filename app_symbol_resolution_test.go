package main

import (
	"bytes"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

const (
	echoPackagePath     = "github.com/brent/echo"
	servicesPackagePath = "github.com/brent/echo/internal/services"
)

func TestAppStartupSymbolsResolve(t *testing.T) {
	fset := token.NewFileSet()
	appFile := parseGoFile(t, fset, "app.go")
	info := typeCheckAppForStartupSymbols(t, fset, appFile)
	startup := requireMethodDecl(t, appFile, "startup")

	startupObj := requireFuncDef(t, info, startup.Name, "method startup")
	requireObjectFile(t, fset, startupObj, "app.go")
	if startupObj.FullName() != "(*github.com/brent/echo.App).startup" {
		t.Fatalf("startup resolved to %q", startupObj.FullName())
	}

	receiver := startup.Recv.List[0].Names[0]
	receiverObj := requireVarDef(t, info, receiver, "receiver a")
	requirePointerToNamed(t, receiverObj.Type(), echoPackagePath, "App", "receiver a")
	requireObjectFile(t, fset, receiverObj, "app.go")

	receiverType := startup.Recv.List[0].Type.(*ast.StarExpr).X.(*ast.Ident)
	appTypeObj := requireTypeUse(t, info, receiverType, "receiver type App")
	requireObjectFile(t, fset, appTypeObj, "app.go")

	param := startup.Type.Params.List[0]
	ctxDecl := param.Names[0]
	ctxObj := requireVarDef(t, info, ctxDecl, "parameter ctx")
	requireNamedType(t, ctxObj.Type(), "context", "Context", "parameter ctx")
	requireObjectFile(t, fset, ctxObj, "app.go")

	contextType := param.Type.(*ast.SelectorExpr)
	requirePackageUse(t, info, contextType.X.(*ast.Ident), "context", "startup parameter package context")
	requireTypeUseWithPackage(t, info, contextType.Sel, "context", "Context", "startup parameter type Context")

	assign := requireAssignStmt(t, startup.Body.List[0])
	assignedField := requireSelectorExpr(t, assign.Lhs[0], "assignment target")
	requireSameObject(t, requireUse(t, info, assignedField.X.(*ast.Ident), "assignment receiver a"), receiverObj, "assignment receiver a")
	ctxField := requireFieldSelection(t, info, assignedField, "ctx", "assignment field ctx")
	requireNamedType(t, ctxField.Type(), "context", "Context", "assignment field ctx")
	requireObjectFile(t, fset, ctxField, "app.go")
	requireSameObject(t, requireUse(t, info, assign.Rhs[0].(*ast.Ident), "assignment right-hand ctx"), ctxObj, "assignment right-hand ctx")

	call := requireCallExpr(t, startup.Body.List[1])
	callee := requireSelectorExpr(t, call.Fun, "SetSystemServiceContext callee")
	requirePackageUse(t, info, callee.X.(*ast.Ident), servicesPackagePath, "services package qualifier")
	setContextObj := requireFuncUse(t, info, callee.Sel, "services.SetSystemServiceContext")
	requireObjectFile(t, fset, setContextObj, filepath.Join("internal", "services", "system.go"))
	requireSetSystemServiceContextSignature(t, setContextObj)

	systemArg := requireSelectorExpr(t, call.Args[0], "SetSystemServiceContext first argument")
	requireSameObject(t, requireUse(t, info, systemArg.X.(*ast.Ident), "call receiver a"), receiverObj, "call receiver a")
	systemField := requireFieldSelection(t, info, systemArg, "System", "App.System field")
	requirePointerToNamed(t, systemField.Type(), servicesPackagePath, "SystemService", "App.System field")
	requireObjectFile(t, fset, systemField, "app.go")
	requireSameObject(t, requireUse(t, info, call.Args[1].(*ast.Ident), "SetSystemServiceContext ctx argument"), ctxObj, "SetSystemServiceContext ctx argument")
}

type startupSymbolImporter struct {
	standard types.Importer
	services *types.Package
}

func (i startupSymbolImporter) Import(path string) (*types.Package, error) {
	if path == servicesPackagePath {
		return i.services, nil
	}
	return i.standard.Import(path)
}

func typeCheckAppForStartupSymbols(t *testing.T, fset *token.FileSet, appFile *ast.File) *types.Info {
	t.Helper()

	standardImporter := importer.Default()
	contextPackage, err := standardImporter.Import("context")
	if err != nil {
		t.Fatalf("import context: %v", err)
	}

	info := &types.Info{
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	config := types.Config{
		Importer: startupSymbolImporter{
			standard: standardImporter,
			services: startupServicesPackage(t, fset, contextPackage),
		},
	}
	if _, err := config.Check(echoPackagePath, fset, []*ast.File{appFile}, info); err != nil {
		t.Fatalf("type-check app.go: %v", err)
	}
	return info
}

func startupServicesPackage(t *testing.T, fset *token.FileSet, contextPackage *types.Package) *types.Package {
	t.Helper()

	systemFile := parseGoFile(t, fset, filepath.Join("internal", "services", "system.go"))
	schedulerFile := parseGoFile(t, fset, filepath.Join("internal", "services", "kanban_scheduler.go"))

	systemServiceDecl := requireTypeDecl(t, systemFile, "SystemService")
	newSystemServiceDecl := requireFunctionDecl(t, fset, systemFile, "", "NewSystemService")
	requireFieldList(t, fset, newSystemServiceDecl.Type.Params, nil, "NewSystemService parameters")
	requireFieldList(t, fset, newSystemServiceDecl.Type.Results, []string{"*SystemService"}, "NewSystemService results")

	setContextDecl := requireFunctionDecl(t, fset, systemFile, "", "SetSystemServiceContext")
	requireFieldList(t, fset, setContextDecl.Type.Params, []string{"service *SystemService", "ctx context.Context"}, "SetSystemServiceContext parameters")
	requireFieldList(t, fset, setContextDecl.Type.Results, nil, "SetSystemServiceContext results")

	shutdownDecl := requireFunctionDecl(t, fset, schedulerFile, "*SystemService", "Shutdown")
	requireFieldList(t, fset, shutdownDecl.Type.Params, nil, "Shutdown parameters")
	requireFieldList(t, fset, shutdownDecl.Type.Results, nil, "Shutdown results")

	contextType := contextPackage.Scope().Lookup("Context")
	if contextType == nil {
		t.Fatal("context.Context was not found")
	}

	pkg := types.NewPackage(servicesPackagePath, "services")
	systemServiceName := types.NewTypeName(systemServiceDecl.Name.Pos(), pkg, "SystemService", nil)
	systemServiceType := types.NewNamed(systemServiceName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(systemServiceName)

	pkg.Scope().Insert(types.NewFunc(
		newSystemServiceDecl.Name.Pos(),
		pkg,
		"NewSystemService",
		types.NewSignatureType(
			nil,
			nil,
			nil,
			nil,
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.NewPointer(systemServiceType))),
			false,
		),
	))

	serviceParam := types.NewVar(setContextDecl.Type.Params.List[0].Names[0].Pos(), pkg, "service", types.NewPointer(systemServiceType))
	ctxParam := types.NewVar(setContextDecl.Type.Params.List[1].Names[0].Pos(), pkg, "ctx", contextType.Type())
	pkg.Scope().Insert(types.NewFunc(
		setContextDecl.Name.Pos(),
		pkg,
		"SetSystemServiceContext",
		types.NewSignatureType(
			nil,
			nil,
			nil,
			types.NewTuple(serviceParam, ctxParam),
			nil,
			false,
		),
	))

	receiver := types.NewVar(shutdownDecl.Recv.List[0].Names[0].Pos(), pkg, "s", types.NewPointer(systemServiceType))
	systemServiceType.AddMethod(types.NewFunc(
		shutdownDecl.Name.Pos(),
		pkg,
		"Shutdown",
		types.NewSignatureType(receiver, nil, nil, nil, nil, false),
	))

	pkg.MarkComplete()
	return pkg
}

func parseGoFile(t *testing.T, fset *token.FileSet, path string) *ast.File {
	t.Helper()

	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func requireMethodDecl(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv != nil && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("method %s was not found", name)
	return nil
}

func requireTypeDecl(t *testing.T, file *ast.File, name string) *ast.TypeSpec {
	t.Helper()

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec := spec.(*ast.TypeSpec)
			if typeSpec.Name.Name == name {
				return typeSpec
			}
		}
	}
	t.Fatalf("type %s was not found", name)
	return nil
}

func requireFunctionDecl(t *testing.T, fset *token.FileSet, file *ast.File, receiver string, name string) *ast.FuncDecl {
	t.Helper()

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name {
			continue
		}
		if receiverString(t, fset, fn) == receiver {
			return fn
		}
	}
	t.Fatalf("function %s with receiver %q was not found", name, receiver)
	return nil
}

func receiverString(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) string {
	t.Helper()

	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return exprString(t, fset, fn.Recv.List[0].Type)
}

func requireFieldList(t *testing.T, fset *token.FileSet, fields *ast.FieldList, expected []string, label string) {
	t.Helper()

	actual := fieldListStrings(t, fset, fields)
	if strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("%s = %v, want %v", label, actual, expected)
	}
}

func fieldListStrings(t *testing.T, fset *token.FileSet, fields *ast.FieldList) []string {
	t.Helper()

	if fields == nil {
		return nil
	}
	output := make([]string, 0, len(fields.List))
	for _, field := range fields.List {
		typeText := exprString(t, fset, field.Type)
		if len(field.Names) == 0 {
			output = append(output, typeText)
			continue
		}
		for _, name := range field.Names {
			output = append(output, name.Name+" "+typeText)
		}
	}
	return output
}

func exprString(t *testing.T, fset *token.FileSet, expr ast.Expr) string {
	t.Helper()

	var buffer bytes.Buffer
	if err := printer.Fprint(&buffer, fset, expr); err != nil {
		t.Fatalf("print expression: %v", err)
	}
	return buffer.String()
}

func requireAssignStmt(t *testing.T, stmt ast.Stmt) *ast.AssignStmt {
	t.Helper()

	assign, ok := stmt.(*ast.AssignStmt)
	if !ok {
		t.Fatalf("expected assignment statement, got %T", stmt)
	}
	return assign
}

func requireCallExpr(t *testing.T, stmt ast.Stmt) *ast.CallExpr {
	t.Helper()

	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		t.Fatalf("expected expression statement, got %T", stmt)
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		t.Fatalf("expected call expression, got %T", expr.X)
	}
	return call
}

func requireSelectorExpr(t *testing.T, expr ast.Expr, label string) *ast.SelectorExpr {
	t.Helper()

	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("%s: expected selector expression, got %T", label, expr)
	}
	return selector
}

func requireFuncDef(t *testing.T, info *types.Info, ident *ast.Ident, label string) *types.Func {
	t.Helper()

	obj, ok := info.Defs[ident].(*types.Func)
	if !ok {
		t.Fatalf("%s: expected function definition for %s, got %T", label, ident.Name, info.Defs[ident])
	}
	return obj
}

func requireVarDef(t *testing.T, info *types.Info, ident *ast.Ident, label string) *types.Var {
	t.Helper()

	obj, ok := info.Defs[ident].(*types.Var)
	if !ok {
		t.Fatalf("%s: expected variable definition for %s, got %T", label, ident.Name, info.Defs[ident])
	}
	return obj
}

func requireUse(t *testing.T, info *types.Info, ident *ast.Ident, label string) types.Object {
	t.Helper()

	obj := info.Uses[ident]
	if obj == nil {
		t.Fatalf("%s: expected use for %s", label, ident.Name)
	}
	return obj
}

func requireFuncUse(t *testing.T, info *types.Info, ident *ast.Ident, label string) *types.Func {
	t.Helper()

	obj, ok := requireUse(t, info, ident, label).(*types.Func)
	if !ok {
		t.Fatalf("%s: expected function use for %s, got %T", label, ident.Name, info.Uses[ident])
	}
	return obj
}

func requireTypeUse(t *testing.T, info *types.Info, ident *ast.Ident, label string) *types.TypeName {
	t.Helper()

	obj, ok := requireUse(t, info, ident, label).(*types.TypeName)
	if !ok {
		t.Fatalf("%s: expected type use for %s, got %T", label, ident.Name, info.Uses[ident])
	}
	return obj
}

func requireTypeUseWithPackage(t *testing.T, info *types.Info, ident *ast.Ident, packagePath string, name string, label string) *types.TypeName {
	t.Helper()

	obj := requireTypeUse(t, info, ident, label)
	if obj.Name() != name || obj.Pkg() == nil || obj.Pkg().Path() != packagePath {
		t.Fatalf("%s: resolved to %s, want %s.%s", label, obj, packagePath, name)
	}
	return obj
}

func requirePackageUse(t *testing.T, info *types.Info, ident *ast.Ident, packagePath string, label string) *types.PkgName {
	t.Helper()

	obj, ok := requireUse(t, info, ident, label).(*types.PkgName)
	if !ok {
		t.Fatalf("%s: expected package use for %s, got %T", label, ident.Name, info.Uses[ident])
	}
	if obj.Imported().Path() != packagePath {
		t.Fatalf("%s: resolved package path %q, want %q", label, obj.Imported().Path(), packagePath)
	}
	return obj
}

func requireFieldSelection(t *testing.T, info *types.Info, selector *ast.SelectorExpr, name string, label string) *types.Var {
	t.Helper()

	selection := info.Selections[selector]
	if selection == nil {
		t.Fatalf("%s: expected selection for %s", label, selector.Sel.Name)
	}
	if selection.Kind() != types.FieldVal {
		t.Fatalf("%s: selection kind = %v, want field", label, selection.Kind())
	}
	field, ok := selection.Obj().(*types.Var)
	if !ok {
		t.Fatalf("%s: expected field object, got %T", label, selection.Obj())
	}
	if field.Name() != name {
		t.Fatalf("%s: resolved field %q, want %q", label, field.Name(), name)
	}
	return field
}

func requireSameObject(t *testing.T, actual types.Object, expected types.Object, label string) {
	t.Helper()

	if actual != expected {
		t.Fatalf("%s: resolved to %s, want %s", label, actual, expected)
	}
}

func requireNamedType(t *testing.T, typ types.Type, packagePath string, name string, label string) {
	t.Helper()

	named, ok := typ.(*types.Named)
	if !ok {
		t.Fatalf("%s: expected named type, got %s", label, typ)
	}
	obj := named.Obj()
	if obj.Name() != name || obj.Pkg() == nil || obj.Pkg().Path() != packagePath {
		t.Fatalf("%s: resolved type %s, want %s.%s", label, typ, packagePath, name)
	}
}

func requirePointerToNamed(t *testing.T, typ types.Type, packagePath string, name string, label string) {
	t.Helper()

	pointer, ok := typ.(*types.Pointer)
	if !ok {
		t.Fatalf("%s: expected pointer type, got %s", label, typ)
	}
	requireNamedType(t, pointer.Elem(), packagePath, name, label)
}

func requireSetSystemServiceContextSignature(t *testing.T, fn *types.Func) {
	t.Helper()

	signature, ok := fn.Type().(*types.Signature)
	if !ok {
		t.Fatalf("SetSystemServiceContext has non-signature type %s", fn.Type())
	}
	if signature.Params().Len() != 2 || signature.Results().Len() != 0 {
		t.Fatalf("SetSystemServiceContext signature = %s", signature)
	}
	requirePointerToNamed(t, signature.Params().At(0).Type(), servicesPackagePath, "SystemService", "SetSystemServiceContext service parameter")
	requireNamedType(t, signature.Params().At(1).Type(), "context", "Context", "SetSystemServiceContext ctx parameter")
}

func requireObjectFile(t *testing.T, fset *token.FileSet, obj types.Object, suffix string) {
	t.Helper()

	position := fset.Position(obj.Pos())
	if !position.IsValid() {
		t.Fatalf("%s has no source position", obj)
	}
	actual := filepath.ToSlash(position.Filename)
	expected := filepath.ToSlash(suffix)
	if actual != expected && !strings.HasSuffix(actual, "/"+expected) {
		t.Fatalf("%s resolved to %s, want suffix %s", obj, position, expected)
	}
}
