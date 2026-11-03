function fromBase64URL(value: string): ArrayBuffer {
  const base64 = value.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(base64 + "=".repeat((4 - base64.length % 4) % 4));
  return Uint8Array.from(binary, (char) => char.charCodeAt(0)).buffer;
}

function toBase64URL(value: ArrayBuffer | null): string | null {
  if (!value) return null;
  const bytes = new Uint8Array(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export async function createPasskey(options: { publicKey: Record<string, unknown> }): Promise<Record<string, unknown>> {
  if (!window.PublicKeyCredential || !navigator.credentials) throw new Error("当前浏览器不支持通行密钥");
  const raw = options.publicKey as unknown as PublicKeyCredentialCreationOptions & { challenge: string; user: { id: string }; excludeCredentials?: { id: string }[] };
  const publicKey: PublicKeyCredentialCreationOptions = {
    ...raw,
    challenge: fromBase64URL(raw.challenge),
    user: { ...raw.user, id: fromBase64URL(String(raw.user.id)) },
    excludeCredentials: raw.excludeCredentials?.map((item) => ({ ...item, id: fromBase64URL(String(item.id)) })),
  };
  const credential = await navigator.credentials.create({ publicKey }) as PublicKeyCredential | null;
  if (!credential) throw new Error("通行密钥创建已取消");
  const response = credential.response as AuthenticatorAttestationResponse;
  return {
    id: credential.id, rawId: toBase64URL(credential.rawId), type: credential.type,
    response: { attestationObject: toBase64URL(response.attestationObject), clientDataJSON: toBase64URL(response.clientDataJSON), transports: response.getTransports?.() ?? [] },
    clientExtensionResults: credential.getClientExtensionResults(),
  };
}

export async function getPasskey(options: { publicKey: Record<string, unknown> }): Promise<Record<string, unknown>> {
  if (!window.PublicKeyCredential || !navigator.credentials) throw new Error("当前浏览器不支持通行密钥");
  const raw = options.publicKey as unknown as PublicKeyCredentialRequestOptions & { challenge: string; allowCredentials?: { id: string }[] };
  const publicKey: PublicKeyCredentialRequestOptions = {
    ...raw,
    challenge: fromBase64URL(raw.challenge),
    allowCredentials: raw.allowCredentials?.map((item) => ({ ...item, id: fromBase64URL(String(item.id)) })),
  };
  const credential = await navigator.credentials.get({ publicKey }) as PublicKeyCredential | null;
  if (!credential) throw new Error("通行密钥验证已取消");
  const response = credential.response as AuthenticatorAssertionResponse;
  return {
    id: credential.id, rawId: toBase64URL(credential.rawId), type: credential.type,
    response: { authenticatorData: toBase64URL(response.authenticatorData), clientDataJSON: toBase64URL(response.clientDataJSON), signature: toBase64URL(response.signature), userHandle: toBase64URL(response.userHandle) },
    clientExtensionResults: credential.getClientExtensionResults(),
  };
}
