package gqlgen_sqlboiler

import (
	"fmt"
	"go/types"
	"io/ioutil"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/codegen/templates"
	"github.com/99designs/gqlgen/plugin"
	"github.com/iancoleman/strcase"
	"github.com/vektah/gqlparser/v2/ast"
	pluralize "github.com/web-ridge/go-pluralize"
)

var (
	pathRegex  *regexp.Regexp    //nolint:gochecknoglobals
	pluralizer *pluralize.Client //nolint:gochecknoglobals
)

func init() { //nolint:gochecknoinits
	pluralizer = pluralize.NewClient()
	pathRegex = regexp.MustCompile(`src/(.*)`)
}

type ModelBuild struct {
	Backend             Config
	Frontend            Config
	HasStringPrimaryIDs bool
	PluginConfig        ConvertPluginConfig
	PackageName         string
	Interfaces          []*Interface
	Models              []*Model
	Enums               []*Enum
	Scalars             []string
}

type Interface struct {
	Description string
	Name        string
}

type Preload struct {
	Key           string
	ColumnSetting ColumnSetting
}

type Model struct { //nolint:maligned
	Name                  string
	PluralName            string
	BoilerModel           *BoilerModel
	PrimaryKeyType        string
	Fields                []*Field
	IsNormal              bool
	IsInput               bool
	IsCreateInput         bool
	IsUpdateInput         bool
	IsNormalInput         bool
	IsPayload             bool
	IsWhere               bool
	IsFilter              bool
	IsPreloadable         bool
	PreloadArray          []Preload
	HasOrganizationID     bool
	HasUserOrganizationID bool
	HasUserID             bool
	HasStringPrimaryID    bool
	// other stuff
	Description string
	PureFields  []*ast.FieldDefinition
	Implements  []string
}

type ColumnSetting struct {
	Name                  string
	RelationshipModelName string
	IDAvailable           bool
}

type Field struct { //nolint:maligned
	Name               string
	JSONName           string
	PluralName         string
	Type               string
	TypeWithoutPointer string
	IsNumberID         bool
	IsPrimaryNumberID  bool
	IsPrimaryID        bool
	IsRequired         bool
	IsPlural           bool
	ConvertConfig      ConvertConfig
	// relation stuff
	IsRelation bool
	// boiler relation stuff is inside this field
	BoilerField BoilerField
	// graphql relation ship can be found here
	Relationship *Model
	IsOr         bool
	IsAnd        bool

	// Some stuff
	Description  string
	OriginalType types.Type
}

type Enum struct {
	Description string
	Name        string

	Values []*EnumValue
}

type EnumValue struct {
	Description string
	Name        string
	NameLower   string
}

func NewConvertPlugin(output, backend, frontend Config, pluginConfig ConvertPluginConfig) plugin.Plugin {
	return &ConvertPlugin{
		Output:         output,
		Backend:        backend,
		Frontend:       frontend,
		PluginConfig:   pluginConfig,
		rootImportPath: getRootImportPath(),
	}
}

type ConvertPlugin struct {
	Output         Config
	Backend        Config
	Frontend       Config
	PluginConfig   ConvertPluginConfig
	rootImportPath string
}

type Config struct {
	Directory   string
	PackageName string
}

type ConvertPluginConfig struct {
	UseReflectWorkaroundForSubModelFilteringInPostgresIssue25 bool
}

var _ plugin.ConfigMutator = &ConvertPlugin{}

func (m *ConvertPlugin) Name() string {
	return "convert-generator"
}

func copyConfig(cfg config.Config) *config.Config {
	return &cfg
}

func GetModelsWithInformation(enums []*Enum, cfg *config.Config, boilerModels []*BoilerModel) []*Model {
	// get models based on the schema and sqlboiler structs
	models := getModelsFromSchema(cfg.Schema, boilerModels)

	// Now we have all model's let enhance them with fields
	enhanceModelsWithFields(enums, cfg.Schema, cfg, models)

	// Add preload maps
	enhanceModelsWithPreloadArray(models)

	// Sort in same order
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	for _, m := range models {
		cfg.Models.Add(m.Name, cfg.Model.ImportPath()+"."+templates.ToGo(m.Name))
	}
	return models
}

func (m *ConvertPlugin) MutateConfig(originalCfg *config.Config) error {
	b := &ModelBuild{
		PackageName: m.Output.PackageName,
		Backend: Config{
			Directory:   path.Join(m.rootImportPath, m.Backend.Directory),
			PackageName: m.Backend.PackageName,
		},
		Frontend: Config{
			Directory:   path.Join(m.rootImportPath, m.Frontend.Directory),
			PackageName: m.Frontend.PackageName,
		},
		PluginConfig: m.PluginConfig,
	}

	cfg := copyConfig(*originalCfg)

	fmt.Println("[convert] get boiler models")
	boilerModels := GetBoilerModels(m.Backend.Directory)

	fmt.Println("[convert] get extra's from schema")
	interfaces, enums, scalars := getExtrasFromSchema(cfg.Schema)

	fmt.Println("[convert] get model with information")
	models := GetModelsWithInformation(enums, originalCfg, boilerModels)

	b.Models = models
	b.HasStringPrimaryIDs = HasStringPrimaryIDsInModels(models)
	b.Interfaces = interfaces
	b.Enums = enums
	b.Scalars = scalars
	if len(b.Models) == 0 {
		fmt.Println("No models found in graphql so skipping generation")
		return nil
	}

	// for _, model := range models {
	// 	fmt.Println(model.Name, "->", model.BoilerModel.Name)
	// 	for _, field := range model.Fields {
	// 		fmt.Println("    ", field.Name, field.Type)
	// 		fmt.Println("    ", field.BoilerField.Name, field.BoilerField.Type)
	// 	}
	// }

	fmt.Println("[convert] render preload.gotpl")
	templates.CurrentImports = nil
	if renderError := templates.Render(templates.Options{
		Template:        getTemplate("preload.gotpl"),
		PackageName:     m.Output.PackageName,
		Filename:        m.Output.Directory + "/" + "preload.go",
		Data:            b,
		GeneratedHeader: true,
		Packages:        cfg.Packages,
	}); renderError != nil {
		fmt.Println("renderError", renderError)
	}
	templates.CurrentImports = nil
	fmt.Println("[convert] render convert.gotpl")
	if renderError := templates.Render(templates.Options{
		Template:        getTemplate("convert.gotpl"),
		PackageName:     m.Output.PackageName,
		Filename:        m.Output.Directory + "/" + "convert.go",
		Data:            b,
		GeneratedHeader: true,
		Packages:        cfg.Packages,
	}); renderError != nil {
		fmt.Println("renderError", renderError)
	}
	templates.CurrentImports = nil
	fmt.Println("[convert] render convert_input.gotpl")
	if renderError := templates.Render(templates.Options{
		Template:        getTemplate("convert_input.gotpl"),
		PackageName:     m.Output.PackageName,
		Filename:        m.Output.Directory + "/" + "convert_input.go",
		Data:            b,
		GeneratedHeader: true,
		Packages:        cfg.Packages,
	}); renderError != nil {
		fmt.Println("renderError", renderError)
	}
	templates.CurrentImports = nil
	fmt.Println("[convert] render filter.gotpl")
	if renderError := templates.Render(templates.Options{
		Template:        getTemplate("filter.gotpl"),
		PackageName:     m.Output.PackageName,
		Filename:        m.Output.Directory + "/" + "filter.go",
		Data:            b,
		GeneratedHeader: true,
		Packages:        cfg.Packages,
	}); renderError != nil {
		fmt.Println("renderError", renderError)
	}

	return nil
}

func getTemplate(filename string) string {
	// load path relative to calling source file
	_, callerFile, _, _ := runtime.Caller(1) //nolint:dogsled
	rootDir := filepath.Dir(callerFile)
	content, err := ioutil.ReadFile(path.Join(rootDir, filename))
	if err != nil {
		fmt.Println("Could not read .gotpl file", err)
		return "Could not read .gotpl file"
	}
	return string(content)
}

func HasStringPrimaryIDsInModels(models []*Model) bool {
	for _, model := range models {
		if model.HasStringPrimaryID {
			return true
		}
	}
	return false
}

// getFieldType check's if user has defined a
func getFieldType(binder *config.Binder, schema *ast.Schema, cfg *config.Config, field *ast.FieldDefinition) (
	types.Type, error) {
	var typ types.Type
	var err error

	fieldDef := schema.Types[field.Type.Name()]
	if cfg.Models.UserDefined(field.Type.Name()) {
		typ, err = binder.FindTypeFromName(cfg.Models[field.Type.Name()].Model[0])
		if err != nil {
			return typ, err
		}
	} else {
		switch fieldDef.Kind {
		case ast.Scalar:
			// no user defined model, referencing a default scalar
			typ = types.NewNamed(
				types.NewTypeName(0, cfg.Model.Pkg(), "string", nil),
				nil,
				nil,
			)

		case ast.Interface, ast.Union:
			// no user defined model, referencing a generated interface type
			typ = types.NewNamed(
				types.NewTypeName(0, cfg.Model.Pkg(), templates.ToGo(field.Type.Name()), nil),
				types.NewInterfaceType([]*types.Func{}, []types.Type{}),
				nil,
			)

		case ast.Enum:
			// no user defined model, must reference a generated enum
			typ = types.NewNamed(
				types.NewTypeName(0, cfg.Model.Pkg(), templates.ToGo(field.Type.Name()), nil),
				nil,
				nil,
			)

		case ast.Object, ast.InputObject:
			// no user defined model, must reference a generated struct
			typ = types.NewNamed(
				types.NewTypeName(0, cfg.Model.Pkg(), templates.ToGo(field.Type.Name()), nil),
				types.NewStruct(nil, nil),
				nil,
			)

		default:
			panic(fmt.Errorf("unknown ast type %s", fieldDef.Kind))
		}
	}

	return typ, err
}

func getGraphqlFieldName(cfg *config.Config, modelName string, field *ast.FieldDefinition) string {
	name := field.Name
	if nameOveride := cfg.Models[modelName].Fields[field.Name].FieldName; nameOveride != "" {
		// TODO: map overrides to sqlboiler the other way around?
		name = nameOveride
	}
	return name
}

func enhanceModelsWithFields(enums []*Enum, schema *ast.Schema, cfg *config.Config, models []*Model) {
	binder := cfg.NewBinder()

	// Generate the basic of the fields
	for _, m := range models {
		// Let's convert the pure ast fields to something usable for our template
		for _, field := range m.PureFields {
			fieldDef := schema.Types[field.Type.Name()]

			// This calls some qglgen boilerType which gets the gqlgen type
			typ, err := getFieldType(binder, schema, cfg, field)
			if err != nil {
				fmt.Println("Could not get field type from graphql schema: ", err)
			}
			jsonName := getGraphqlFieldName(cfg, m.Name, field)
			name := getGoFieldName(jsonName)

			// just some (old) Relay clutter which is not needed anymore + we won't do anything with it
			// in our database converts.
			if strings.EqualFold(name, "clientMutationId") {
				continue
			}

			// override type struct with qqlgen code
			typ = binder.CopyModifiersFromAst(field.Type, typ)
			if isStruct(typ) && (fieldDef.Kind == ast.Object || fieldDef.Kind == ast.InputObject) {
				typ = types.NewPointer(typ)
			}

			// generate some booleans because these checks will be used a lot
			isRelation := fieldDef.Kind == ast.Object || fieldDef.Kind == ast.InputObject

			shortType := getShortType(typ.String())

			isPrimaryID := strings.EqualFold(name, "id")

			// get sqlboiler information of the field
			boilerField := findBoilerFieldOrForeignKey(m.BoilerModel.Fields, name, isRelation)
			isString := strings.Contains(strings.ToLower(boilerField.Type), "string")
			isNumberID := strings.HasSuffix(name, "ID") && !isString
			isPrimaryNumberID := isPrimaryID && !isString

			isPrimaryStringID := isPrimaryID && isString
			// enable simpler code in resolvers

			if isPrimaryStringID {
				m.HasStringPrimaryID = isPrimaryStringID
			}
			if isPrimaryNumberID || isPrimaryStringID {
				m.PrimaryKeyType = boilerField.Type
			}

			// log some warnings when fields could not be converted
			if boilerField.Type == "" {
				// TODO: add filter + where here
				switch {
				case m.IsPayload:
				case pluralizer.IsPlural(name):
				case (m.IsFilter || m.IsWhere) && (strings.EqualFold(name, "and") ||
					strings.EqualFold(name, "or") ||
					strings.EqualFold(name, "search") ||
					strings.EqualFold(name, "where")):
				default:
					{
						fmt.Println("[WARN] boiler type not available for ", name)
					}
				}
			}

			if boilerField.Name == "" {
				if m.IsPayload || m.IsFilter || m.IsWhere {
				} else {
					fmt.Println("[WARN] boiler name not available for ", m.Name+"."+name)
					continue
				}
			}
			field := &Field{
				Name:               name,
				JSONName:           jsonName,
				Type:               shortType,
				TypeWithoutPointer: strings.Replace(strings.TrimPrefix(shortType, "*"), ".", "Dot", -1),
				BoilerField:        boilerField,
				IsNumberID:         isNumberID,
				IsPrimaryID:        isPrimaryID,
				IsPrimaryNumberID:  isPrimaryNumberID,
				IsRelation:         isRelation,
				IsOr:               strings.EqualFold(name, "or"),
				IsAnd:              strings.EqualFold(name, "and"),
				IsPlural:           pluralizer.IsPlural(name),
				PluralName:         pluralizer.Plural(name),
				OriginalType:       typ,
				Description:        field.Description,
			}
			field.ConvertConfig = getConvertConfig(enums, m, field)
			m.Fields = append(m.Fields, field)
		}
	}

	for _, m := range models {
		m.HasOrganizationID = findField(m.Fields, "organizationId") != nil
		m.HasUserOrganizationID = findField(m.Fields, "userOrganizationId") != nil
		m.HasUserID = findField(m.Fields, "userId") != nil
		for _, f := range m.Fields {
			if f.BoilerField.Relationship != nil {
				f.Relationship = findModel(models, f.BoilerField.Relationship.Name)
			}
		}
	}
}

func getGoFieldName(name string) string {
	goFieldName := strcase.ToCamel(name)
	// in golang Id = ID
	goFieldName = strings.Replace(goFieldName, "Id", "ID", -1)
	// in golang Url = URL
	goFieldName = strings.Replace(goFieldName, "Url", "URL", -1)
	return goFieldName
}

var ignoreTypePrefixes = []string{"graphql_models", "models", "boilergql"} //nolint:gochecknoglobals

func getShortType(longType string) string {
	// longType e.g = gitlab.com/decicify/app/backend/graphql_models.FlowWhere
	splittedBySlash := strings.Split(longType, "/")
	// gitlab.com, decicify, app, backend, graphql_models.FlowWhere

	lastPart := splittedBySlash[len(splittedBySlash)-1]
	isPointer := strings.HasPrefix(longType, "*")
	isStructInPackage := strings.Count(lastPart, ".") > 0

	if isStructInPackage {
		// if packages are deeper they don't have pointers but *time.Time will since it's not deep
		returnType := strings.TrimPrefix(lastPart, "*")
		for _, ignoreType := range ignoreTypePrefixes {
			fullIgnoreType := ignoreType + "."
			returnType = strings.TrimPrefix(returnType, fullIgnoreType)
		}

		if isPointer {
			return "*" + returnType
		}
		return returnType
	}

	return longType
}

func findModel(models []*Model, search string) *Model {
	for _, m := range models {
		if m.Name == search {
			return m
		}
	}
	return nil
}

func findField(fields []*Field, search string) *Field {
	for _, f := range fields {
		if f.Name == search {
			return f
		}
	}
	return nil
}

func findBoilerFieldOrForeignKey(fields []*BoilerField, golangGraphQLName string, isRelation bool) BoilerField {
	// get database friendly struct for this model
	for _, field := range fields {
		if isRelation {
			// If it a relation check to see if a foreign key is available
			if strings.EqualFold(field.Name, golangGraphQLName+"ID") {
				return *field
			}
		}
		if strings.EqualFold(field.Name, golangGraphQLName) {
			return *field
		}
	}

	// // fallback on foreignKey

	// }

	// fmt.Println("???", golangGraphQLName)

	return BoilerField{}
}

func getExtrasFromSchema(schema *ast.Schema) (interfaces []*Interface, enums []*Enum, scalars []string) {
	for _, schemaType := range schema.Types {
		switch schemaType.Kind {
		case ast.Interface, ast.Union:
			interfaces = append(interfaces, &Interface{
				Description: schemaType.Description,
				Name:        schemaType.Name,
			})
		case ast.Enum:
			it := &Enum{
				Name: schemaType.Name,

				Description: schemaType.Description,
			}
			for _, v := range schemaType.EnumValues {
				it.Values = append(it.Values, &EnumValue{
					Name:        v.Name,
					NameLower:   strcase.ToLowerCamel(strings.ToLower(v.Name)),
					Description: v.Description,
				})
			}
			if strings.HasPrefix(it.Name, "_") {
				continue
			}
			enums = append(enums, it)
		case ast.Scalar:
			scalars = append(scalars, schemaType.Name)
		}
	}
	return
}

func getModelsFromSchema(schema *ast.Schema, boilerModels []*BoilerModel) (models []*Model) {
	for _, schemaType := range schema.Types {
		// skip boiler plate from ggqlgen, we only want the models
		if strings.HasPrefix(schemaType.Name, "_") {
			continue
		}

		// if cfg.Models.UserDefined(schemaType.Name) {
		// 	fmt.Println("continue")
		// 	continue
		// }

		switch schemaType.Kind {
		case ast.Object, ast.InputObject:
			{
				if schemaType == schema.Query ||
					schemaType == schema.Mutation ||
					schemaType == schema.Subscription {
					continue
				}
				modelName := schemaType.Name

				// fmt.Println("GRAPHQL MODEL ::::", m.Name)
				if strings.HasPrefix(modelName, "_") {
					continue
				}

				// We will try to find a corresponding boiler struct
				boilerModel := FindBoilerModel(boilerModels, getBaseModelFromName(modelName))

				isInput := strings.HasSuffix(modelName, "Input") && modelName != "Input"
				isCreateInput := strings.HasSuffix(modelName, "CreateInput") && modelName != "CreateInput"
				isUpdateInput := strings.HasSuffix(modelName, "UpdateInput") && modelName != "UpdateInput"
				isFilter := strings.HasSuffix(modelName, "Filter") && modelName != "Filter"
				isWhere := strings.HasSuffix(modelName, "Where") && modelName != "Where"
				isPayload := strings.HasSuffix(modelName, "Payload") && modelName != "Payload"

				// if no boiler model is found
				if boilerModel == nil || boilerModel.Name == "" {
					if isInput || isWhere || isFilter || isPayload {
						// silent continue
						continue
					}

					fmt.Printf("[WARN] Skip %v because no database model found\n", modelName)
					continue
				}

				isNormalInput := isInput && !isCreateInput && !isUpdateInput

				m := &Model{
					Name:          modelName,
					Description:   schemaType.Description,
					PluralName:    pluralizer.Plural(modelName),
					BoilerModel:   boilerModel,
					IsInput:       isInput,
					IsFilter:      isFilter,
					IsWhere:       isWhere,
					IsUpdateInput: isUpdateInput,
					IsCreateInput: isCreateInput,
					IsNormalInput: isNormalInput,
					IsPayload:     isPayload,
					IsNormal:      !isInput && !isWhere && !isFilter && !isPayload,
					IsPreloadable: !isInput && !isWhere && !isFilter && !isPayload,
				}

				for _, implementor := range schema.GetImplements(schemaType) {
					m.Implements = append(m.Implements, implementor.Name)
				}

				m.PureFields = append(m.PureFields, schemaType.Fields...)
				models = append(models, m)
			}
		}
	}
	return //nolint:nakedret
}

func getPreloadMapForModel(model *Model) map[string]ColumnSetting {
	preloadMap := map[string]ColumnSetting{}
	for _, field := range model.Fields {
		// only relations are preloadable
		if !field.IsRelation {
			continue
		}
		// var key string
		// if field.IsPlural {
		key := field.JSONName
		// } else {
		// 	key = field.PluralName
		// }
		name := fmt.Sprintf("models.%vRels.%v", model.Name, foreignKeyToRel(field.BoilerField.Name))
		setting := ColumnSetting{
			Name:                  name,
			IDAvailable:           !field.IsPlural,
			RelationshipModelName: field.BoilerField.Relationship.TableName,
		}

		preloadMap[key] = setting
	}
	return preloadMap
}

func enhanceModelsWithPreloadArray(models []*Model) {
	// first adding basic first level relations
	for _, model := range models {
		if !model.IsPreloadable {
			continue
		}

		modelPreloadMap := getPreloadMapForModel(model)

		sortedPreloadKeys := make([]string, 0, len(modelPreloadMap))
		for k := range modelPreloadMap {
			sortedPreloadKeys = append(sortedPreloadKeys, k)
		}
		sort.Strings(sortedPreloadKeys)

		model.PreloadArray = make([]Preload, len(sortedPreloadKeys))
		for i, k := range sortedPreloadKeys {
			columnSetting := modelPreloadMap[k]
			model.PreloadArray[i] = Preload{
				Key:           k,
				ColumnSetting: columnSetting,
			}
		}
	}
}

// The relationship is defined in the normal model but not in the input, where etc structs
// So just find the normal model and get the relationship type :)
func getBaseModelFromName(v string) string {
	v = safeTrim(v, "CreateInput")
	v = safeTrim(v, "UpdateInput")
	v = safeTrim(v, "Input")
	v = safeTrim(v, "Payload")
	v = safeTrim(v, "Where")
	v = safeTrim(v, "Filter")
	return v
}

func safeTrim(v string, trimSuffix string) string {
	// let user still choose Payload as model names
	// not recommended but could be done theoretically :-)
	if v != trimSuffix {
		v = strings.TrimSuffix(v, trimSuffix)
	}
	return v
}

func foreignKeyToRel(v string) string {
	return strings.TrimSuffix(strcase.ToCamel(v), "ID")
}

func isStruct(t types.Type) bool {
	_, is := t.Underlying().(*types.Struct)
	return is
}

type ConvertConfig struct {
	IsCustom         bool
	ToBoiler         string
	ToGraphQL        string
	GraphTypeAsText  string
	BoilerTypeAsText string
}

func findEnum(enums []*Enum, graphType string) *Enum {
	for _, enum := range enums {
		if enum.Name == graphType {
			return enum
		}
	}
	return nil
}

func getConvertConfig(enums []*Enum, model *Model, field *Field) (cc ConvertConfig) { //nolint:nakedret
	graphType := field.Type
	boilType := field.BoilerField.Type

	enum := findEnum(enums, field.TypeWithoutPointer)
	if enum != nil { //nolint:nestif
		cc.IsCustom = true
		cc.ToBoiler = strings.TrimPrefix(
			getToBoiler(
				getBoilerTypeAsText(boilType),
				getGraphTypeAsText(graphType),
			), "boilergql.")

		cc.ToGraphQL = strings.TrimPrefix(
			getToGraphQL(
				getBoilerTypeAsText(boilType),
				getGraphTypeAsText(graphType),
			), "boilergql.")
	} else if graphType != boilType {
		cc.IsCustom = true
		if field.IsPrimaryNumberID || field.IsNumberID && field.BoilerField.IsRelation {
			cc.ToGraphQL = "VALUE"
			cc.ToBoiler = "VALUE"

			// first unpointer json type if is pointer
			if strings.HasPrefix(graphType, "*") {
				cc.ToBoiler = "boilergql.PointerStringToString(VALUE)"
			}

			goToUint := getBoilerTypeAsText(boilType) + "ToUint"
			if goToUint == "IntToUint" {
				cc.ToGraphQL = "uint(VALUE)"
			} else if goToUint != "UintToUint" {
				cc.ToGraphQL = "boilergql." + goToUint + "(VALUE)"
			}

			if field.IsPrimaryNumberID {
				cc.ToGraphQL = model.Name + "IDToGraphQL(" + cc.ToGraphQL + ")"
			} else if field.IsNumberID {
				cc.ToGraphQL = field.BoilerField.Relationship.Name + "IDToGraphQL(" + cc.ToGraphQL + ")"
			}

			isInt := strings.HasPrefix(strings.ToLower(boilType), "int") && !strings.HasPrefix(strings.ToLower(boilType), "uint")

			if strings.HasPrefix(boilType, "null") {
				cc.ToBoiler = fmt.Sprintf("boilergql.IDToNullBoiler(%v)", cc.ToBoiler)
				if isInt {
					cc.ToBoiler = fmt.Sprintf("boilergql.NullUintToNullInt(%v)", cc.ToBoiler)
				}
			} else {
				cc.ToBoiler = fmt.Sprintf("boilergql.IDToBoiler(%v)", cc.ToBoiler)
				if isInt {
					cc.ToBoiler = fmt.Sprintf("int(%v)", cc.ToBoiler)
				}
			}

			cc.ToGraphQL = strings.Replace(cc.ToGraphQL, "VALUE", "m."+field.BoilerField.Name, -1)
			cc.ToBoiler = strings.Replace(cc.ToBoiler, "VALUE", "m."+field.Name, -1)
		} else {
			// Make these go-friendly for the helper/convert.go package
			cc.ToBoiler = getToBoiler(getBoilerTypeAsText(boilType), getGraphTypeAsText(graphType))
			cc.ToGraphQL = getToGraphQL(getBoilerTypeAsText(boilType), getGraphTypeAsText(graphType))
		}
	}
	// fmt.Println("boilType for", field.Name, ":", boilType)

	cc.GraphTypeAsText = getGraphTypeAsText(graphType)
	cc.BoilerTypeAsText = getBoilerTypeAsText(boilType)

	return //nolint:nakedret
}

func getToBoiler(boilType, graphType string) string {
	return "boilergql." + getGraphTypeAsText(graphType) + "To" + getBoilerTypeAsText(boilType)
}

func getToGraphQL(boilType, graphType string) string {
	return "boilergql." + getBoilerTypeAsText(boilType) + "To" + getGraphTypeAsText(graphType)
}

func getBoilerTypeAsText(boilType string) string {
	// backward compatible missed Dot
	if strings.HasPrefix(boilType, "types.") {
		boilType = strings.TrimPrefix(boilType, "types.")
		boilType = strcase.ToCamel(boilType)
		boilType = "Types" + boilType
	}

	// if strings.HasPrefix(boilType, "null.") {
	// 	boilType = strings.TrimPrefix(boilType, "null.")
	// 	boilType = strcase.ToCamel(boilType)
	// 	boilType = "NullDot" + boilType
	// }
	boilType = strings.Replace(boilType, ".", "Dot", -1)

	return strcase.ToCamel(boilType)
}

func getGraphTypeAsText(graphType string) string {
	if strings.HasPrefix(graphType, "*") {
		graphType = strings.TrimPrefix(graphType, "*")
		graphType = strcase.ToCamel(graphType)
		graphType = "Pointer" + graphType
	}
	return strcase.ToCamel(graphType)
}
