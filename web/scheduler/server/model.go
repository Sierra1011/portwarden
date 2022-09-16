package server

import (
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/RichardKnop/machinery/v1/tasks"
	"github.com/Sierra1011/portwarden"
	"github.com/Sierra1011/portwarden/web"
	"golang.org/x/oauth2"
)

const (
	ErrWillNotSetupBackupByUser = "err the user stopped backing up"
)

type BackupSetting struct {
	Passphrase             string `json:"passphrase"`
	BackupFrequencySeconds int    `json:"backup_frequency_seconds"`
	WillSetupBackup        bool   `json:"will_setup_backup"`
}

type DecryptBackupInfo struct {
	File       *multipart.FileHeader `form:"file"`
	Passphrase string                `form:"passphrase"`
}

type GoogleTokenVerifyResponse struct {
	IssuedTo      string `json:"issued_to"`
	Audience      string `json:"audience"`
	UserID        string `json:"user_id"`
	Scope         string `json:"scope"`
	ExpiresIn     int64  `json:"expires_in"`
	Email         string `json:"email"`
	VerifiedEmail bool   `json:"verified_email"`
	AccessType    string `json:"access_type"`
}

type GoogleDriveCredentials struct {
	State string `form:"state"`
	Code  string `form:"code"`
	Scope string `form:"scope"`
}

type PortwardenUser struct {
	Email                     string                       `json:"email"`
	BitwardenDataJSON         []byte                       `json:"bitwarden_data_json"`
	BitwardenSessionKey       string                       `json:"bitwarden_session_key"`
	BackupSetting             BackupSetting                `json:"backup_setting"`
	BitwardenLoginCredentials *portwarden.LoginCredentials `json:"bitwarden_login_credentials"` // Not stored in Redis
	GoogleUserInfo            GoogleUserInfo
	GoogleToken               *oauth2.Token
}

type GoogleUserInfo struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
	Link       string `json:"link"`
	Picture    string `json:"picture"`
	Locale     string `json:"locale"`
}

func (pu *PortwardenUser) CreateWithGoogle() error {
	var err error
	pu.GoogleUserInfo, err = RetrieveUserEmail(pu.GoogleToken)
	if err != nil {
		return err
	}
	pu.Email = pu.GoogleUserInfo.Email
	err = pu.Set()
	if err != nil {
		return err
	}
	return nil
}

func (pu *PortwardenUser) LoginWithBitwarden() error {
	web.GlobalMutex.Lock()
	defer web.GlobalMutex.Unlock()
	var err error
	pu.BitwardenSessionKey, pu.BitwardenDataJSON, err = portwarden.BWLoginGetSessionKeyAndDataJSON(pu.BitwardenLoginCredentials, web.BITWARDENCLI_APPDATA_DIR)
	if err != nil {
		return err
	}
	return nil
}

func (pu *PortwardenUser) SetupAutomaticBackup(eta *time.Time) error {
	signature := &tasks.Signature{
		Name: "BackupToGoogleDrive",
		Args: []tasks.Arg{
			{
				Type:  "string",
				Value: pu.Email,
			},
		},
		ETA:        eta,
		RetryCount: web.MachineryRetryCount,
	}
	_, err := web.MachineryServer.SendTask(signature)
	if err != nil {
		return err
	}
	return nil
}

func (pu *PortwardenUser) Set() error {
	// Encrypt the passphrase
	encryptedPassphraseBytes, err := portwarden.EncryptBytes([]byte(pu.BackupSetting.Passphrase), portwarden.Salt)
	if err != nil {
		return err
	}
	pu.BackupSetting.Passphrase = b64.StdEncoding.EncodeToString(encryptedPassphraseBytes)
	// Clear bitwarden login credentials so we don't store them
	pu.BitwardenLoginCredentials = &portwarden.LoginCredentials{}
	puJson, err := json.Marshal(pu)
	if err != nil {
		return err
	}
	err = web.RedisClient.Set(pu.Email, string(puJson), 0).Err()
	if err != nil {
		return err
	}
	return nil
}

func (pu *PortwardenUser) Get() error {
	val, err := web.RedisClient.Get(pu.Email).Result()
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(val), &pu); err != nil {
		return err
	}
	// Decrypt the passphrase
	encryptedPassphraseBytes, err := b64.StdEncoding.DecodeString(pu.BackupSetting.Passphrase)
	if err != nil {
		return err
	}
	decryptedPassphraseBytes, err := portwarden.DecryptBytes(encryptedPassphraseBytes, portwarden.Salt)
	if err != nil {
		return err
	}
	pu.BackupSetting.Passphrase = string(decryptedPassphraseBytes)
	return nil
}

func VerifyGoogleAccessToekn(access_token string) (bool, error) {
	url := "https://www.googleapis.com/oauth2/v1/tokeninfo?access_token=" + access_token
	response, err := http.Get(url)
	defer response.Body.Close()
	if err != nil {
		return false, err
	}
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return false, err
	}
	var gtvr GoogleTokenVerifyResponse
	if err := json.Unmarshal(body, &gtvr); err != nil {
		return false, err
	}
	if !gtvr.VerifiedEmail {
		return false, errors.New(string(body))
	}
	return true, nil
}

func RetrieveUserEmail(token *oauth2.Token) (GoogleUserInfo, error) {
	var gui GoogleUserInfo
	postURL := "https://www.googleapis.com/oauth2/v2/userinfo"
	request, err := http.NewRequest("GET", postURL, nil)
	if err != nil {
		return gui, err
	}
	request.Header.Add("Host", "www.googleapis.com")
	request.Header.Add("Authorization", "Bearer "+token.AccessToken)
	request.Header.Add("Content-Length", strconv.FormatInt(request.ContentLength, 10))

	GoogleDriveClient := web.GoogleDriveAppConfig.Client(oauth2.NoContext, token)
	response, err := GoogleDriveClient.Do(request)
	if err != nil {
		return gui, err
	}

	defer response.Body.Close()
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return gui, err
	}
	if err := json.Unmarshal(body, &gui); err != nil {
		return gui, err
	}
	return gui, nil
}
