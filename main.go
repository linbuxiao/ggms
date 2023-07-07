package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/joho/godotenv"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/afero"
	"github.com/urfave/cli/v2"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"os"
	"path"
	"reflect"
)

const (
	__NAME__           = "GGMS"
	__USAGE__          = "mongo schema util"
	__VERSION__        = "0.0.1"
	__DESCRIPTION__    = "get the schema in a mongo collection"
	__DEFAULT_CONFIG__ = `
MONGO_URI=mongodb://localhost:27017
MONGO_DATABASE_NAME=test
MONGO_COLLECTION_NAME=test
# sometimes there is too much data in a collection and we cannot traverse all the records. In this case you can select a flag column, where the different contents of the column correspond to the different structure of the records. An example is event.
# The tool will select the most recent record in the matching flag column for analysis. This will speed things up.
MONGO_KEY_COLUMN=event
`
)

func main() {
	app := &cli.App{
		Name:        __NAME__,
		Version:     __VERSION__,
		Usage:       __USAGE__,
		Description: __DESCRIPTION__,
		Commands: []*cli.Command{
			{
				Name:  "init",
				Usage: "create a config file at the specified path, or create a config file at the default path if no path is specified",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "config_path",
						Aliases: []string{"c"},
					},
				},
				Action: func(cCtx *cli.Context) error {
					var (
						configPath string
						err        error
					)
					configPath = cCtx.String("config_path")
					if configPath == "" {
						configPath, err = getDefaultConfigPath()
						if err != nil {
							return err
						}
					}
					appfs := afero.NewOsFs()
					exists, err := afero.Exists(appfs, configPath)
					if err != nil {
						return err
					}
					if exists {
						return errors.New("config file already exists")
					} else {
						// create config file
						e, err := afero.DirExists(appfs, path.Dir(configPath))
						if err != nil {
							return err
						}
						if !e {
							if err = appfs.MkdirAll(path.Dir(configPath), 0755); err != nil {
								return err
							}
						}
					}
					f, err := appfs.Create(configPath)
					if err != nil {
						return err
					}
					if _, err = f.WriteString(__DEFAULT_CONFIG__); err != nil {
						return err
					}
					fmt.Println("config file created at: " + configPath)
					return nil
				},
			},
			{
				Name:  "run",
				Usage: "run the tool, you can specify the path of the config file, or use the default path.",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "config_path",
						Aliases: []string{"c"},
					},
					&cli.StringFlag{
						Name:    "output_format",
						Aliases: []string{"o"},
					},
				},
				Action: func(cCtx *cli.Context) error {
					configPath := cCtx.String("config_path")
					if configPath == "" {
						var err error
						configPath, err = getDefaultConfigPath()
						if err != nil {
							return err
						}
					}
					format := cCtx.String("output_format")
					if err := godotenv.Load(configPath); err != nil {
						return err
					}
					uri := os.Getenv("MONGO_URI")
					if uri == "" {
						return errors.New("You must set your 'MONGODB_URI' environmental variable. See\n\t https://www.mongodb.com/docs/drivers/go/current/usage-examples/#environment-variable")
					}
					client, err := mongo.Connect(cCtx.Context, options.Client().ApplyURI(uri))
					dataBaseName := os.Getenv("MONGO_DATABASE_NAME")
					collectionName := os.Getenv("MONGO_COLLECTION_NAME")
					coll := client.Database(dataBaseName).Collection(collectionName)
					if err != nil {
						return err
					}
					engine := &Engine{
						Ctx:          cCtx.Context,
						Collection:   coll,
						KeyColumn:    os.Getenv("MONGO_KEY_COLUMN"),
						OutputFormat: format,
					}
					result, err := engine.Run()
					if err != nil {
						return err
					}
					return engine.Render(result)
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

type Engine struct {
	Ctx          context.Context
	Collection   *mongo.Collection
	KeyColumn    string
	OutputFormat string
}

func (e *Engine) Run() (map[string]string, error) {
	result := make(map[string]string, 0)
	if e.KeyColumn != "" {
		allKeyColumn, err := e.Collection.Distinct(
			e.Ctx,
			e.KeyColumn,
			bson.M{},
		)
		if err != nil {
			return result, err
		}
		for _, keyColumn := range allKeyColumn {
			filter := bson.M{
				e.KeyColumn: keyColumn,
			}
			doc := e.Collection.FindOne(e.Ctx, filter, options.FindOne().SetSort(bson.M{"_id": -1}))
			if doc.Err() != nil {
				return result, doc.Err()
			}
			var data bson.M
			if err := doc.Decode(&data); err != nil {
				return result, err
			}
			for k, v := range data {
				result[k] = reflect.TypeOf(v).String()
			}
		}
	}
	return result, nil
}

func (e *Engine) Render(result map[string]string) error {
	return renderFactory(result, e.OutputFormat).Render()
}

type Render interface {
	Render() error
}

type RenderTable struct {
	Rows [][]string
}

func (r *RenderTable) Render() error {
	renderer := tablewriter.NewWriter(os.Stdout)
	headers := []string{"name", "type"}
	renderer.SetHeader(headers)
	for _, row := range r.Rows {
		renderer.Append(row)
	}
	renderer.Render()
	return nil
}

type RenderJSON struct {
	Data map[string]string
}

func (r *RenderJSON) Render() error {
	typeMap := map[string]string{
		"string":      "string",
		"float64":     "string",
		"primitive.M": "object",
		"primitive.A": "array",
	}
	arr := make([]map[string]string, 0)
	for k, v := range r.Data {
		t, ok := typeMap[v]
		if !ok {
			t = "string"
		}
		arr = append(arr, map[string]string{"name": k, "type": t})
	}
	content, err := json.Marshal(arr)
	if err != nil {
		return err
	}
	fmt.Println(string(content))
	return nil
}

func renderFactory(data map[string]string, format string) Render {
	if format == "json" {
		return &RenderJSON{Data: data}
	}
	rows := make([][]string, 0)
	for k, v := range data {
		rows = append(rows, []string{k, v})
	}
	return &RenderTable{
		Rows: rows,
	}
}

func getDefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return dir + "/ggms/.env", nil
}
