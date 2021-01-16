// wpaste - easy code sharing
// Copyright (C) 2020  Evgeniy Rybin
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
package main

import (
	"bytes"
	"encoding/gob"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/gomarkdown/markdown"
	"github.com/gorilla/mux"
	"go.etcd.io/bbolt"
)

const charset = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// RandomString creates a random string with the charset
// that contains all letters and digits
func RandomString(length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// WpasteFile is data about file
type WpasteFile struct {
	id             uint64
	Name           string
	Data           string
	AccessPassword string
	EditPassword   string
	// Created is time in UTC and UnixNano when file created
	Created      int64
	// ExpiresAfter is time in nanoseconds that must pass after Created
	// so that file is expired
	ExpiresAfter int64
	// Edited is time in UTC and UnixNano when file edited
	Edited       int64
}

// Serialize enocde WpasteFile to bytes
func (w *WpasteFile) Serialize() ([]byte, error) {
	var result bytes.Buffer
	err := gob.NewEncoder(&result).Encode(w)
	return result.Bytes(), err
}

// DeserializeWpasteFile decode bytes to WpasteFile
func DeserializeWpasteFile(d []byte) (*WpasteFile, error) {
	var wpaste WpasteFile

	err := gob.NewDecoder(bytes.NewReader(d)).Decode(&wpaste)

	return &wpaste, err
}

// Expired return true if file expired
func (w *WpasteFile) Expired() bool {
	if w.ExpiresAfter != 0 {
		return time.Now().UTC().UnixNano() > w.Created+w.ExpiresAfter
	}
	return false
}

// Exist return true if file found and exist
func (w *WpasteFile) Exist() bool {
	return w != nil
}

// AllowAccess return true if access password is empty or
// entered password matches access password
func (w *WpasteFile) AllowAccess(password string) bool {
	if len(w.AccessPassword) == 0 || password == w.AccessPassword {
		return true
	}
	return false
}

// AllowEdit return true if entered password matches access password
// if edit password is empty always return false
func (w *WpasteFile) AllowEdit(password string) bool {
	if len(w.EditPassword) == 0 || password != w.EditPassword {
		return false
	}
	return true
}

// Save file to db
func (w *WpasteFile) Save() (err error) {
	tx, err := db.Begin(true)
	if err != nil {
		return
	}
	defer tx.Rollback()

	files := tx.Bucket([]byte("files"))

	f, err := w.Serialize()
	if err != nil {
		return
	}

	if w.id == 0 {
		w.id, _ = files.NextSequence()
	}

	files.Put([]byte(strconv.FormatUint(w.id, 10)), f)
	return tx.Commit()
}

// Delete file from database
func (w *WpasteFile) Delete() error {
	return db.Update(func(tx *bbolt.Tx) error {
		files := tx.Bucket([]byte("files"))

		return files.Delete([]byte(strconv.FormatUint(w.id, 10)))
	})
}

// OpenWpasteByName return Wpaste if exist else nil
func OpenWpasteByName(name string) (file *WpasteFile, err error) {
	tx, err := db.Begin(false)
	if err != nil {
		return
	}
	defer tx.Rollback()
	files := tx.Bucket([]byte("files"))
	for id := files.Sequence(); id > 0; id-- {
		v := files.Get([]byte(strconv.FormatUint(id, 10)))
		if len(v) == 0 {
			continue
		}
		var f *WpasteFile
		f, err = DeserializeWpasteFile(v)
		if err != nil {
			log.Println(err)
			return
		}
		if f.Name == name {
			file = f
			file.id = id
			return
		}
	}
	return
}

// CheckUnique return true to *unique if value unique
func CheckUnique(field string, value interface{}) (unique bool) {
	tx, err := db.Begin(false)
	if err != nil {
		return
	}
	defer tx.Rollback()

	unique = true

	files := tx.Bucket([]byte("files"))
	cur := files.Cursor()

	for k, v := cur.First(); k != nil; k, v = cur.Next() {
		var f *WpasteFile
		f, err = DeserializeWpasteFile(v)
		if err != nil {
			continue
		}
		field := reflect.ValueOf(*f).FieldByName(field)
		if field.String() == value {
			unique = false
			break
		}
	}

	return
}

// HTTPError write status code to header and description to body
func HTTPError(w http.ResponseWriter, code int, description string) {
	w.WriteHeader(code)
	w.Write([]byte(description))
}

// HTTPServerError id equivalent for HTTPError which write http.StatusInternalServerError
func HTTPServerError(w http.ResponseWriter) {
	HTTPError(w, http.StatusInternalServerError, "500 - Something bad happened")
}

// Help return README.md
func Help(w http.ResponseWriter, r *http.Request) {
	file, err := ioutil.ReadFile("README.md")
	if err != nil {
		log.Println(err)
		HTTPServerError(w)
		return
	}
	w.Write([]byte(markdown.ToHTML(file, nil, nil)))
}

// UploadFile save file and response it ID
func UploadFile(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 2<<20 {
		HTTPError(w, http.StatusRequestEntityTooLarge, "413 - Max content size is 2MiB")
		return
	} else if len(r.FormValue("f")) == 0 {
		HTTPError(w, http.StatusBadRequest, `400 - "f" field required`)
		return
	}

	wpaste := &WpasteFile{Created: time.Now().UTC().UnixNano()}

	wpaste.Data = r.FormValue("f")

	name := r.FormValue("name")

	if len(name) == 0 {
		name = RandomString(3)
		for !CheckUnique("Name", name) {
			name = RandomString(3)
		}
	} else {
		if !CheckUnique("Name", name) {
			HTTPError(w, http.StatusConflict, "409 - This filename already taken!")
			return
		}
	}
	wpaste.Name = name

	e := r.FormValue("e")
	if len(e) != 0 {
		addTime, err := strconv.ParseInt(e, 10, 64)
		if err != nil {
			HTTPError(w, http.StatusUnprocessableEntity, "422 - Invalid time format")
			return
		} else if addTime < 0 {
			HTTPError(w, http.StatusBadRequest, "400 - Time shold be positive")
			return
		}
		wpaste.ExpiresAfter = addTime * int64(time.Second)
	}

	wpaste.AccessPassword = r.FormValue("ap")
	wpaste.EditPassword = r.FormValue("ep")

	if err := wpaste.Save(); err != nil {
		HTTPServerError(w)
		return
	}

	w.Write([]byte(name))
}

// SendFile respond file by it ID
func SendFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	ID := vars["id"]
	file, err := OpenWpasteByName(ID)
	if err != nil {
		HTTPServerError(w)
		return
	}
	r.ParseForm()
	if !file.Exist() {
		HTTPError(w, http.StatusNotFound, "404 - File not found")
		return
	} else if file.Expired() {
		HTTPError(w, http.StatusGone, "410 - File is no longer available")
		return
	} else if !file.AllowAccess(r.Form.Get("ap")) {
		HTTPError(w, http.StatusUnauthorized, "401 - Invalid password")
		return
	}
	w.Write([]byte((*file).Data))
}

// EditFile put new file
func EditFile(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 10<<20 {
		HTTPError(w, http.StatusRequestEntityTooLarge, "413 - Max content size is 2MiB")
		return
	} else if len(r.FormValue("f")) == 0 {
		HTTPError(w, http.StatusBadRequest, `400 - "f" field required`)
	}
	vars := mux.Vars(r)
	ID := vars["id"]

	file, err := OpenWpasteByName(ID)
	if err != nil {
		HTTPServerError(w)
		return
	}

	if !file.Exist() {
		HTTPError(w, http.StatusNotFound, "404 - File not found")
		return
	} else if file.Expired() {
		HTTPError(w, http.StatusGone, "410 - File is no longer available")
		return
	} else if !file.AllowEdit(r.FormValue("ep")) {
		HTTPError(w, http.StatusUnauthorized, "401 - Invalid password")
		return
	}

	file.Data = r.FormValue("f")
	file.Edited = time.Now().UTC().UnixNano()

	if err := file.Save(); err != nil {
		HTTPServerError(w)
		return
	}
}

// DeleteFile set deleted flag to true
func DeleteFile(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	ID := vars["id"]

	file, err := OpenWpasteByName(ID)
	if err != nil {
		HTTPServerError(w)
		return
	}

	r.ParseForm()
	if !file.Exist() {
		HTTPError(w, http.StatusNotFound, "404 - File not found")
		return
	} else if !file.AllowEdit(r.FormValue("ep")) {
		HTTPError(w, http.StatusUnauthorized, "401 - Invalid password")
		return
	}

	if err := file.Delete(); err != nil {
		HTTPServerError(w)
		return
	}
}

// WpasteRouter make router with all needed Handlers
func WpasteRouter() *mux.Router {
	Router := mux.NewRouter().StrictSlash(true)

	Router.HandleFunc("/", Help).Methods("GET")
	Router.HandleFunc("/", UploadFile).Methods("POST")

	Router.HandleFunc("/{id}", SendFile).Methods("GET")
	Router.HandleFunc("/{id}", EditFile).Methods("PUT")
	Router.HandleFunc("/{id}", DeleteFile).Methods("DELETE")
	return Router
}

// AutoDeleter delete file from db if it expired "add" time ago
// and check using timer
func AutoDeleter(timer *time.Ticker, add int64) {
	for range timer.C {
		var toDelete [][]byte
		db.View(func(tx *bbolt.Tx) error {
			files := tx.Bucket([]byte("files"))

			files.ForEach(func(k, v []byte) error {
				if len(v) == 0 {
					return nil
				}
				var f *WpasteFile
				f, err := DeserializeWpasteFile(v)
				if err != nil {
					return err
				}
				if f.ExpiresAfter != 0 && time.Now().UTC().UnixNano() > f.Created+f.ExpiresAfter+add {
					toDelete = append(toDelete, k)
				}
				return nil
			})
			return nil
		})

		if len(toDelete) != 0 {
			db.Update(func(tx *bbolt.Tx) error {
				files := tx.Bucket([]byte("files"))

				for _, id := range toDelete {
					files.Delete(id)
				}
				return nil
			})
		}
	}
}

var db *bbolt.DB

func initDB(name string) {
	var err error
	db, err = bbolt.Open(name, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("files"))
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
}

func logging(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			var addr string
			// for nginx
			if len(r.Header.Get("X-Real-IP")) != 0 {
				addr = r.Header.Get("X-Real-IP")
			} else {
				addr = r.RemoteAddr
			}
			log.Println(r.Method, r.URL.Path, addr, r.UserAgent())
		}()
		handler.ServeHTTP(w, r)
	})
}

func run(dbname string, tick time.Duration, add int64, start bool) {
	rand.Seed(time.Now().UTC().UnixNano())

	initDB(dbname)

	go AutoDeleter(time.NewTicker(tick), add)

	if start {
		defer db.Close()
		http.ListenAndServe(":9990", logging(WpasteRouter()))
	}
}

func main() {
	f, err := os.OpenFile("log.wpaste", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)
	run("data.db", time.Hour, 4*int64(time.Hour), true)
}
