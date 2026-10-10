#!/bin/sh
# Makes the self-signed code-signing identity `just app` signs Regrow.app
# with, and records its SHA-1 in .signing-identity. macOS ties Full Disk
# Access and the other grants to the signature, so one identity kept
# across rebuilds keeps them.
#
#   scripts/signing-cert.sh            create it, or find the one that exists
#   scripts/signing-cert.sh --dry-run  say what would happen; the keychain is only read
#
# The first signature with the new key raises a keychain dialog: choose
# "Always Allow". A re-run creates nothing and prints the same hash.
set -eu

NAME="regrow local signing"
DAYS=3653
ROOT=$(cd "$(dirname "$0")/.." && pwd)
RECORD="$ROOT/.signing-identity"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"
OPENSSL=/usr/bin/openssl

dry=0
case "${1:-}" in
"") ;;
--dry-run) dry=1 ;;
*)
	echo "usage: $0 [--dry-run]" >&2
	exit 64
	;;
esac

# SHA-1 of every certificate in the keychain whose name is NAME.
existing() {
	security find-certificate -a -c "$NAME" -Z "$KEYCHAIN" 2>/dev/null |
		awk -v want="\"alis\"<blob>=\"$NAME\"" '
			/^SHA-1 hash:/ { hash = $3 }
			index($0, want) { print hash }'
}

# Whether the keychain holds the private key too: a certificate alone signs nothing.
has_key() {
	security find-identity -p codesigning "$KEYCHAIN" 2>/dev/null | grep -q "$1"
}

record() {
	if [ "$dry" -eq 1 ]; then
		echo "dry run: would write $1 to $RECORD"
		return
	fi
	printf '%s\n' "$1" >"$RECORD"
}

found=$(existing)
count=$(printf '%s' "$found" | grep -c . || true)

if [ "$count" -gt 1 ]; then
	echo "The keychain holds $count certificates named \"$NAME\":" >&2
	printf '  %s\n' $found >&2
	echo "Delete all but one in Keychain Access (login, My Certificates), then run this again." >&2
	exit 1
fi

if [ "$count" -eq 1 ]; then
	if ! has_key "$found"; then
		echo "A certificate named \"$NAME\" exists ($found) but its private key is not in the keychain." >&2
		echo "Delete it in Keychain Access, then run this again." >&2
		exit 1
	fi
	record "$found"
	echo "$found"
	echo "The identity \"$NAME\" already exists; nothing was created."
	exit 0
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/regrow-signing.XXXXXX")
trap 'rm -rf "${work:?}"' EXIT
umask 077

cat >"$work/req.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $NAME
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
EOF

"$OPENSSL" req -x509 -newkey rsa:2048 -nodes -days "$DAYS" -config "$work/req.cnf" \
	-keyout "$work/key.pem" -out "$work/cert.pem" 2>/dev/null
sha1=$("$OPENSSL" x509 -in "$work/cert.pem" -noout -fingerprint -sha1 | sed 's/.*=//; s/://g')

if [ "$dry" -eq 1 ]; then
	echo "dry run: no identity named \"$NAME\" in $KEYCHAIN."
	echo "dry run: a throwaway certificate was made to check openssl, and is deleted now:"
	"$OPENSSL" x509 -in "$work/cert.pem" -noout -subject -enddate -text |
		grep -E 'subject=|notAfter=|Code Signing|Digital Signature|CA:FALSE' | sed 's/^ */  /'
	echo "dry run: would import a new one with: security import <p12> -k $KEYCHAIN -T /usr/bin/codesign"
	record "<its SHA-1>"
	exit 0
fi

# The passphrase only guards the file between these two commands.
pass=$("$OPENSSL" rand -hex 16)
"$OPENSSL" pkcs12 -export -name "$NAME" -inkey "$work/key.pem" -in "$work/cert.pem" \
	-out "$work/identity.p12" -passout "pass:$pass"
security import "$work/identity.p12" -k "$KEYCHAIN" -P "$pass" -T /usr/bin/codesign >/dev/null

if ! has_key "$sha1"; then
	echo "The import ran, but the keychain does not list $sha1 as a signing identity." >&2
	exit 1
fi

# One signature now, while someone is watching for the keychain dialog.
cp /usr/bin/true "$work/probe"
echo "Signing a scratch file with the new key. If a keychain dialog appears, choose \"Always Allow\"."
if ! codesign --force --sign "$sha1" "$work/probe"; then
	echo "codesign could not use the new identity ($sha1). It stays in the keychain; nothing was recorded." >&2
	exit 1
fi

record "$sha1"
echo "$sha1"
echo "Created \"$NAME\", valid $DAYS days, and recorded it in $RECORD."
