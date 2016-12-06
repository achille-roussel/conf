package conf

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/segmentio/jutil"
)

// Load the program's configuration into dst, and returns the list of arguments
// that were not used.
//
// The dst argument is expected to be a pointer to a struct type where exported
// fields or fields with a "conf" tag will be used to load the program
// configuration.
// The function panics if dst is not a pointer to struct, or if it's a nil
// pointer.
//
// The configuration is loaded from the command line, environment and optional
// configuration file if the -config-file option is present in the program
// arguments.
//
// Values found in the progrma arguments take precendence over those found in
// the environment, which takes precendence over the configuration file.
//
// If an error is detected with the configurable the function print the usage
// message to stdout and exit with status code 1.
func Load(dst interface{}) (args []string) {
	var err error

	if args, err = (Loader{
		Args:     os.Args[1:],
		Env:      os.Environ(),
		Program:  filepath.Base(os.Args[0]),
		FileFlag: "config-file",
	}).Load(dst); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(1)
	}

	return
}

// A Loader can be used to provide a costomized configurable for loading a
// configuration.
type Loader struct {
	Args     []string // list of arguments
	Env      []string // list of environment variables ["KEY=VALUE", ...]
	Program  string   // name of the program
	FileFlag string   // command line option for the configuration file
}

// Load uses the loader ld to load the program configuration into dst, and
// returns the list of program arguments that were not used.
//
// The dst argument is expected to be a pointer to a struct type where exported
// fields or fields with a "conf" tag will be used to load the program
// configuration.
// The function panics if dst is not a pointer to struct, or if it's a nil
// pointer.
func (ld Loader) Load(dst interface{}) (args []string, err error) {
	v := reflect.ValueOf(dst)

	if v.Kind() != reflect.Ptr {
		panic(fmt.Sprintf("cannot load configuration into %T", dst))
	}

	if v.IsNil() {
		panic(fmt.Sprintf("cannot load configuration into nil %T", dst))
	}

	if v = v.Elem(); v.Kind() != reflect.Struct {
		panic(fmt.Sprintf("cannot load configuration into %T", dst))
	}

	return ld.load(v)
}

func (ld Loader) load(dst reflect.Value) (args []string, err error) {
	if err = loadFile(dst, ld.Program, ld.FileFlag, ld.Args, ioutil.ReadFile); err != nil {
		args = nil
		return
	}

	if err = loadEnv(dst, ld.Program, ld.Env); err != nil {
		args = nil
		return
	}

	return loadArgs(dst, ld.Program, ld.FileFlag, ld.Args)
}

func loadFile(dst reflect.Value, name string, fileFlag string, args []string, readFile func(string) ([]byte, error)) (err error) {
	if len(fileFlag) != 0 {
		var a = append([]string{}, args...)
		var b []byte
		var f string
		var v = reflect.New(dst.Type()).Elem()

		out := &bytes.Buffer{}
		set := flag.NewFlagSet(name, flag.ContinueOnError)
		set.SetOutput(out)
		set.StringVar(&f, fileFlag, "", "Path to the configuration file.")

		scanFields(v, "", ".", func(key string, help string, val reflect.Value) {
			set.Var(value{val}, key, help)
		})

		if err = set.Parse(a); err != nil {
			return
		}

		if len(f) == 0 {
			return
		}

		if b, err = readFile(f); err != nil {
			return
		}

		if err = yaml.Unmarshal(b, dst.Addr().Interface()); err != nil {
			return
		}
	}
	return
}

func loadEnv(dst reflect.Value, name string, env []string) (err error) {
	type entry struct {
		key string
		val value
	}
	var entries []entry

	scanFields(dst, name, "_", func(key string, help string, val reflect.Value) {
		entries = append(entries, entry{
			key: snakecaseUpper(key) + "=",
			val: value{val},
		})
	})

	for _, e := range entries {
		for _, kv := range env {
			if strings.HasPrefix(kv, e.key) {
				if err = e.val.Set(kv[len(e.key):]); err != nil {
					return
				}
				break
			}
		}
	}

	return
}

func loadArgs(dst reflect.Value, name string, fileFlag string, args []string) (leftover []string, err error) {
	args = append([]string{}, args...)

	out := &bytes.Buffer{}
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(out)

	if len(fileFlag) != 0 {
		set.String(fileFlag, "", "Path to the configuration file.")
	}

	scanFields(dst, "", ".", func(key string, help string, val reflect.Value) {
		set.Var(value{val}, key, help)
	})

	if err = set.Parse(args); err != nil {
		return
	}

	leftover = set.Args()
	return
}

type value struct {
	v reflect.Value
}

func (f value) String() string {
	if !f.v.IsValid() {
		return ""
	}
	b, _ := json.Marshal(f.v.Interface())
	return string(b)
}

func (f value) Get() interface{} {
	if f.v.IsValid() {
		return nil
	}
	return f.v.Interface()
}

func (f value) Set(s string) error {
	return yaml.Unmarshal([]byte(s), f.v.Addr().Interface())
}

func (f value) IsBoolFlag() bool {
	return f.v.IsValid() && f.v.Kind() == reflect.Bool
}

func scanFields(v reflect.Value, base string, sep string, do func(string, string, reflect.Value)) {
	t := v.Type()

	for i, n := 0, v.NumField(); i != n; i++ {
		ft := t.Field(i)
		fv := v.Field(i)

		name := ft.Name
		help := ft.Tag.Get("help")
		jtag := jutil.ParseTag(ft.Tag.Get("json"))

		if jtag.Skip {
			continue
		}

		if len(jtag.Name) != 0 {
			name = jtag.Name
		}

		if len(base) != 0 {
			name = base + sep + name
		}

		// Dereference all pointers and create objects on the ones that are nil.
		for fv.Kind() == reflect.Ptr {
			if fv.IsNil() {
				fv.Set(reflect.New(ft.Type.Elem()))
			}
			fv = fv.Elem()
		}

		// Inner structs are flattened to allow composition of configuration
		// objects.
		if fv.Kind() == reflect.Struct {
			scanFields(fv, name, sep, do)
			continue
		}

		// For all other field types the delegate is called.
		do(name, help, fv)
	}
}