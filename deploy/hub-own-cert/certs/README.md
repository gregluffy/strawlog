# Certificates

Put `strawlog.crt` and `strawlog.key` here. Neither file is committed.

**Requirements**
- The certificate needs a **SAN** (Subject Alternative Name) for the hostname people type, e.g.
  `DNS:logs.corp.example`. Browsers ignore the CN.
- `strawlog.crt`: PEM, the server certificate first, then any intermediate CA certificates. The root
  CA does not need to be in it: domain-joined clients already trust it (GPO).
- `strawlog.key`: PEM, **unencrypted**.
- The container runs with `cap_drop: ALL`, so it can only read the key if root owns it or it is
  group/world readable: `sudo chown root:root strawlog.key && sudo chmod 600 strawlog.key`.

**From an AD CS `.pfx` / `.p12` export**

```bash
openssl pkcs12 -in strawlog.pfx -clcerts -nokeys -out server.crt
openssl pkcs12 -in strawlog.pfx -cacerts -nokeys -out chain.crt     # intermediates (+ root; harmless)
cat server.crt chain.crt > strawlog.crt
openssl pkcs12 -in strawlog.pfx -nocerts -nodes -out strawlog.key
```

Check the pair: `openssl x509 -in strawlog.crt -noout -subject -ext subjectAltName -enddate`.

**Requesting one with a CSR**

```bash
openssl req -new -newkey rsa:2048 -nodes -keyout strawlog.key -out strawlog.csr \
  -subj "/CN=logs.corp.example" -addext "subjectAltName=DNS:logs.corp.example"
# submit strawlog.csr to your CA with the "Web Server" template, save the result as strawlog.crt
```
