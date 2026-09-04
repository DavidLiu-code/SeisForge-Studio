# Limage v1.3.1 — signed six-month licensing

## Security model
Limage now uses an offline, machine-bound Ed25519 signed license.

- Each PC receives a 16-character machine code such as `ABCD-EFGH-IJKL-MNOP`.
- A registration code is signed by the owner's Ed25519 **private key**.
- The distributed Limage EXE contains only the matching **public key**.
- A code issued for one machine cannot be reused on another machine.
- Each newly generated code expires after **six calendar months**.
- Limage performs an offline last-seen-clock check to catch ordinary attempts to move the system clock backwards.
- The current licensee and expiry date are shown in About.

## Activation workflow
1. User starts Limage.
2. If no valid license exists, Limage displays its machine code.
3. User sends the machine code to the software owner.
4. Owner uses the private administrator tool to generate a registration code.
5. User pastes the code into Limage and clicks Activate.
6. The license is stored under `%LOCALAPPDATA%\Limage\license.lic`.

## Administrator usage
Keep these two files together on the owner's private computer only:

- `Limage_License_Generator.exe`
- `limage_ed25519_private.key`

Example:

```text
Limage_License_Generator.exe -machine ABCD-EFGH-IJKL-MNOP -name "Customer Name"
```

To save the code to a file:

```text
Limage_License_Generator.exe -machine ABCD-EFGH-IJKL-MNOP -name "Customer Name" -out customer.lic
```

The generator prints the issue date and the expiry date. Expiry is six calendar months from generation.

## Critical warning
**Never distribute `limage_ed25519_private.key` or the administrator package.**
Anyone who obtains the private key can issue valid Limage licenses.
Back up the key securely. If it is lost, future releases using the same embedded public key cannot issue compatible new licenses.

## Limitations
No purely offline licensing scheme can make a native desktop application impossible to crack. A determined reverse engineer can patch license checks in the executable. This design is intended to prevent normal copying/sharing and make unauthorized key generation cryptographically infeasible without the private key.

For stronger commercial protection, the next layer should be optional online activation/revocation plus Authenticode code signing.
