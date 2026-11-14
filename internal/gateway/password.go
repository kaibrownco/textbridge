package gateway

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const pbkdf2Iterations = 600_000

// Words used to build a memorable passphrase (~7.8 bits each).
var passWords = strings.Fields(`
acorn amber anchor apple arrow aspen atlas autumn badge bamboo banjo barley
basin beacon berry birch bison blaze bloom bluff bonfire breeze brick bridge
brook buffalo butter cabin cactus camel canoe canyon cargo cedar chalk cherry
cider citrus clover cobalt comet copper coral cotton cougar coyote crane
creek cricket crystal cypress daisy delta denim desert dolphin dove dragon
drift dune eagle echo ember falcon fennel fern fiddle field finch flint
forest fossil fox galaxy garnet geyser ginger glacier granite grove gull
harbor harvest hawk hazel heron hickory honey horizon husky iris island ivory
jade jasper juniper kayak kettle kiwi koala lagoon lantern larch lark lava
lemon lilac linen lotus lynx magnet mango maple marble meadow mesa mint
moose moss mustang nectar nickel nutmeg oasis ocean olive onyx opal orchard
otter owl oyster paddle palm panda pebble pepper petal pine pinto plum
pollen pony poppy prairie quail quartz rabbit radar raven reef ridge river
robin rocket rose ruby saddle sage salmon sandal sapphire scarlet sequoia
shadow shell sierra silver slate sparrow spruce squash star stone summit
sunset swan tango thistle thunder tiger timber topaz trail tulip tundra
turtle valley velvet violet walnut willow wolf wren yarrow zephyr zinc
anvil aurora bayou breaker cascade cobble dawn ferry glade hollow jetty
kelp knoll ledge marsh mirage nimbus orbit prism rapids sable tide
`)

// GeneratePassphrase returns a random 6-word passphrase plus a 2-digit number
// (~53 bits), easy to type on a phone.
func GeneratePassphrase() string {
	parts := make([]string, 0, 7)
	for i := 0; i < 6; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passWords))))
		if err != nil {
			panic(err)
		}
		parts = append(parts, passWords[n.Int64()])
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(90))
	parts = append(parts, strconv.Itoa(int(n.Int64())+10))
	return strings.Join(parts, "-")
}

// HashPassword returns "pbkdf2-sha256$iter$salt$hash".
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func passwordPath(dir string) string { return filepath.Join(dir, "password") }

func LoadPasswordHash(dir string) (string, error) {
	b, err := os.ReadFile(passwordPath(dir))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func SavePasswordHash(dir, hash string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(passwordPath(dir), []byte(hash+"\n"), 0o600)
}
