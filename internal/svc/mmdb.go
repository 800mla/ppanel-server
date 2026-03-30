package svc

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/geoip2-golang"
	"github.com/perfect-panel/server/pkg/logger"
)

const GeoIPDBURL = "https://raw.githubusercontent.com/adysec/IP_database/main/geolite/GeoLite2-City.mmdb"

var geoIPHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

type IPLocation struct {
	Path string
	DB   *geoip2.Reader
}

func NewIPLocation(path string) (*IPLocation, error) {
	if err := ensureGeoIPDatabase(path); err != nil {
		return nil, err
	}

	db, err := geoip2.Open(path)
	if err != nil {
		return nil, err
	}
	return &IPLocation{
		Path: path,
		DB:   db,
	}, nil
}

func (ipLoc *IPLocation) Close() error {
	return ipLoc.DB.Close()
}

func ensureGeoIPDatabase(path string) error {
	db, err := geoip2.Open(path)
	if err == nil {
		return db.Close()
	}

	if _, statErr := os.Stat(path); statErr == nil {
		logger.Errorf("[GeoIP] Existing database is invalid, recreating %s: %v", path, err)
		if removeErr := os.Remove(path); removeErr != nil {
			return fmt.Errorf("remove invalid GeoIP database %s: %w", path, removeErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	logger.Infof("[GeoIP] Database not found, downloading from %s", GeoIPDBURL)
	if err := DownloadGeoIPDatabase(GeoIPDBURL, path); err != nil {
		logger.Errorf("[GeoIP] Failed to download database: %v", err.Error())
		return err
	}
	logger.Infof("[GeoIP] Database downloaded successfully")
	return nil
}

func DownloadGeoIPDatabase(url, path string) error {
	baseDir := filepath.Dir(path)
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		logger.Errorf("[GeoIP] Failed to create directory: %v", err.Error())
		return err
	}

	resp, err := geoIPHTTPClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code when downloading GeoIP database: %s", resp.Status)
	}

	tmpFile, err := os.CreateTemp(baseDir, "GeoLite2-City-*.mmdb")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	keepTemp := false
	defer func() {
		_ = tmpFile.Close()
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	written, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		return err
	}
	if written == 0 {
		return errors.New("downloaded GeoIP database is empty")
	}
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	db, err := geoip2.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("validate GeoIP database %s: %w", tmpPath, err)
	}
	if err := db.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	keepTemp = true
	return nil
}
