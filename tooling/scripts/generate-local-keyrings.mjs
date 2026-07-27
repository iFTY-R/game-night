import { generateKeyPairSync, randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, writeFileSync } from "node:fs";
import { isAbsolute, resolve, join } from "node:path";

const targetDirectory = process.argv[2];

if (!targetDirectory || !isAbsolute(targetDirectory)) {
  throw new Error("usage: node generate-local-keyrings.mjs <absolute-directory>");
}

const directory = resolve(targetDirectory);
mkdirSync(directory, { recursive: true });

const createdAt = new Date().toISOString();

const writeReadOnly = (filename, document) => {
  const path = join(directory, filename);
  if (existsSync(path)) {
    return;
  }
  writeFileSync(path, `${JSON.stringify(document, null, 2)}\n`, { encoding: "utf8", flag: "wx", mode: 0o400 });
  chmodSync(path, 0o400);
  process.stdout.write(`created local keyring: ${filename}\n`);
};

const symmetricKeyrings = [
  "pii-keyring.json",
  "totp-keyring.json",
  "result-envelope-keyring.json",
  "device-keyring.json",
  "rate-limit-keyring.json",
  "user-challenge-keyring.json",
  "admin-challenge-keyring.json",
  "admin-session-keyring.json",
  "admin-cursor-keyring.json"
];

for (const filename of symmetricKeyrings) {
  writeReadOnly(filename, {
    active_version: 1,
    keys: [{ version: 1, key: randomBytes(32).toString("base64"), not_before: createdAt }]
  });
}

const { publicKey, privateKey } = generateKeyPairSync("ed25519");
const publicKeyBytes = publicKey.export({ type: "spki", format: "der" }).subarray(-32);
const seed = privateKey.export({ type: "pkcs8", format: "der" }).subarray(-32);
const privateKeyBytes = Buffer.concat([seed, publicKeyBytes]);

writeReadOnly("audit-keyring.json", {
  active_version: 1,
  keys: [{
    version: 1,
    public_key: publicKeyBytes.toString("base64"),
    private_key: privateKeyBytes.toString("base64"),
    not_before: createdAt
  }]
});
