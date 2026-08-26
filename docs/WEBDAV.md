# Mounting Storix as a drive

Storix serves the folders you already have access to over WebDAV, so they can
be opened from Windows Explorer, the macOS Finder or a Linux file manager and
worked with like any other drive. Nothing is copied to your machine, and the
same containment, protected locations and read only rules apply as in the
browser.

Developed by X Project.

## Before you start

**The address** is your Storix address followed by `/dav/`, for example
`https://files.example.com/dav/` or `http://185.12.34.56:8686/dav/`. Both
`/dav` and `/dav/` are answered.

**The top level** holds one collection per folder your account may reach, or
every served folder if you are an administrator. Each is named after its label
reduced to a path segment: lower case, with spaces and punctuation collapsed
into single dashes, cut to 64 characters. A folder labelled `Web root` appears
as `web-root`. Two labels that reduce to the same name are told apart with a
number, and the name is matched without regard to case.

**The credentials** are your Storix username with either an access token or
your account password. A token is the better answer: it is revoked on its own
without changing the password or signing anybody out, and one created with the
`read` scope mounts read only whatever the account is otherwise allowed to do.
Create a token in the interface, on the access tokens panel, and copy it when
it is shown, because it is never shown again. The username has to be the
account the token belongs to, so a token presented under someone else's name is
refused rather than quietly landing in the wrong place.

Two kinds of account cannot use the password form at all and have to mount with
a token: one with two factor sign in, which has no way to answer the challenge
over Basic credentials, and one that has been told to change its password at
next sign in.

**Use HTTPS if you can.** Point a domain at the server, enter it in Settings,
Domain and HTTPS, and a certificate is issued for you. Every operating system
treats a WebDAV mount over plain HTTP as a downgrade, and Windows refuses one
outright until a registry value is changed.

## What the server answers

OPTIONS, PROPFIND, PROPPATCH, GET, HEAD, POST, PUT, DELETE, MKCOL, COPY, MOVE,
LOCK and UNLOCK. It reports `DAV: 1, 2`, so a client that insists on taking a
lock before it writes, which both Windows and the Finder do, will mount.
PROPPATCH is answered but never accepts a change, which is why timestamps are
not carried across; see what a mount does not do, below.

GET on a collection is answered with 405. A browser pointed at `/dav/` shows an
error rather than a folder listing, which is normal and not a sign that
anything is wrong. Use a real client to check.

## Windows

From a command prompt:

```
net use Z: http://SERVER:8686/dav/ /user:alice PASTE_TOKEN /persistent:yes
```

Or through Explorer: open **This PC**, choose **Map network drive**, pick a
letter, enter `http://SERVER:8686/dav/` as the folder, tick **Connect using
different credentials**, and give your username and the token when asked.

To disconnect:

```
net use Z: /delete
```

### Windows refuses Basic authentication over plain HTTP

This is the one thing that catches everybody. The Windows WebClient service
ships with `BasicAuthLevel` set to 1, which allows Basic credentials only over
SSL. Against a plain HTTP address the mount fails with a system error 67, or
asks for the password again and again no matter what you type.

The right fix is HTTPS. If you genuinely cannot have it yet, for example on a
private network during a migration, run this in an **elevated** command prompt
and try again:

```
reg add "HKLM\SYSTEM\CurrentControlSet\Services\WebClient\Parameters" /v BasicAuthLevel /t REG_DWORD /d 2 /f
net stop webclient
net start webclient
```

Setting it to 2 tells Windows to send Basic credentials over unencrypted
connections as well, which means your token crosses the network in a form
anyone on the path can read. Set up HTTPS and put the value back to 1.

### The 50 MB transfer ceiling

The same service refuses to read a file larger than about 50 MB through a
mapped drive, and reports error 0x800700DF. Raise the ceiling to the maximum of
4 GB, again from an elevated prompt:

```
reg add "HKLM\SYSTEM\CurrentControlSet\Services\WebClient\Parameters" /v FileSizeLimitInBytes /t REG_DWORD /d 4294967295 /f
net stop webclient
net start webclient
```

Above 4 GB, Windows has no answer. Use the Storix web interface for those
files: the upload there is resumable and has no ceiling of its own.

## macOS

In the Finder, open the **Go** menu and choose **Connect to Server**, or press
**Command K**. Enter the address:

```
http://SERVER:8686/dav/
```

Click **Connect**, choose **Registered User**, and give your Storix username
with the token as the password. Tick the keychain box to be asked only once.
The drive appears in the Finder sidebar and under `/Volumes`.

The same thing from Terminal:

```bash
sudo mkdir -p /Volumes/Storix
sudo mount_webdav -i -v Storix http://SERVER:8686/dav/ /Volumes/Storix
```

`-i` makes it ask for the username and the token. To unmount:

```bash
umount /Volumes/Storix
```

## Linux

### davfs2

```bash
sudo apt install davfs2
sudo mkdir -p /mnt/storix
sudo mount -t davfs http://SERVER:8686/dav/ /mnt/storix
```

It asks for the username and the token. To mount without being asked, put the
credentials in the secrets file, which only root may read. The first field is
either the mount point or the address:

```bash
echo "/mnt/storix alice PASTE_TOKEN" | sudo tee -a /etc/davfs2/secrets
sudo chmod 600 /etc/davfs2/secrets
```

For a mount that survives a reboot and can be started by a normal user, add
this line to `/etc/fstab`:

```
http://SERVER:8686/dav/ /mnt/storix davfs _netdev,noauto,user,uid=1000,gid=1000 0 0
```

Then `mount /mnt/storix` is enough. Unmount with `sudo umount /mnt/storix`.
The `user` option only works for someone in the `davfs2` group, so add yourself
with `sudo usermod -aG davfs2 $USER` and sign in again.

### rclone

rclone is the better tool when you want to copy and sync rather than browse,
and it does not need root. Create the remote in one command:

```bash
rclone config create storix webdav \
  url http://SERVER:8686/dav/ \
  vendor other \
  user alice \
  pass PASTE_TOKEN
```

rclone obscures the token as it writes it, so the configuration file never
holds it in the clear. This is what lands in `~/.config/rclone/rclone.conf`,
which you can also write by hand; to fill in `pass` yourself, run
`rclone obscure 'PASTE_TOKEN'` and paste what it prints:

```ini
[storix]
type = webdav
url = http://SERVER:8686/dav/
vendor = other
user = alice
pass = OBSCURED_TOKEN_FROM_RCLONE_OBSCURE
```

Check it, then use it:

```bash
rclone lsd storix:
rclone copy /home/alice/backups storix:/data/backups --progress
```

`rclone lsd storix:` prints the top level collections, which are the names to
use in a path. To have it as a folder as well:

```bash
mkdir -p ~/storix
rclone mount storix: ~/storix --vfs-cache-mode writes --daemon
```

## What a mount does not do

A drive is the files themselves and little else. These are the differences that
matter in practice, rather than a list of everything the interface can do.

- **A delete is permanent.** Removing something from a mounted drive removes it
  from the disk. The recycle bin belongs to the web interface and the API, so
  nothing deleted through the drive can be restored from it.
- **Timestamps are not preserved.** Storix refuses PROPPATCH, which is how a
  client asks for the original creation and modification times after a copy, so
  a file arrives carrying the time it was written. The refusal is per property
  and clients carry on regardless, but the dates on the server are the dates of
  the copy.
- **Moving between two top level folders is refused.** Each one is pinned by
  its own directory handle at the syscall level, so there is no safe single
  step between them. Copy the item across and delete the original instead.
  Moving anywhere within one top level folder works normally.
- **Some refusals arrive with no explanation.** Where Storix can name the
  reason, it answers a write with a sentence: this folder is read only, this
  account is not allowed to delete. Where the refusal comes from the file
  system itself, all the protocol carries is a status code, and a file manager
  usually renders that as a generic failure with no detail at all.
- **A storage allowance is enforced, but explains itself badly.** Writes stop
  once the allowance is spent. WebDAV has no status for that, so the client is
  told 404 when the refusal comes before the first byte and 405 when it comes
  part way through a copy. An unexplained failure on an account near its limit
  is almost always the limit.
- **Successful operations are not in the Security log.** Refused sign ins and
  refused writes are audited; a file copied or deleted through the drive is
  not. Turn on `access_log` in `/etc/storix/config.yaml` to see the requests
  themselves.
- **There is no resumable upload.** A transfer interrupted at 79 GB starts
  again from zero, which is what the tus upload in the web interface exists to
  avoid. Use the browser, or rclone, for very large transfers.
- **Locks are held in memory.** Windows and macOS take a lock before writing,
  so Storix answers LOCK, but a restart of the service releases every lock.
  This matters only if two people edit the same file at the same moment.
- **The interface keeps its own features.** Thumbnails, previews, share links,
  the transfer queue, the duplicate finder and the editor are things you open
  in a browser.

What a mount does keep is the containment. It reaches exactly the folders your
account may reach and nothing else, and the protected locations refused in the
browser are refused here too.

## Troubleshooting

**It asks for the password over and over.** On Windows this is almost always
`BasicAuthLevel`, described above. Everywhere else, check that the username
matches the account the token was created for, that the token has not expired
and has not been revoked, and that the account is still active. An account with
two factor sign in cannot use its password here at all and needs a token.

**Everything is read only.** Three things cause it, in this order. The token
has the `read` scope, so create a `write` one. The folder is mounted read only
for your account, which an administrator sets per folder. Or the account does
not carry the upload permission, which turns every one of its folders read only
on the drive. The browser shows the same restriction, which is a quick way to
tell whether the mount is the problem or the account is.

**One kind of change is refused and the rest work.** The drive applies the same
permissions the browser does, one per operation: adding a file needs upload,
a new folder needs create, deleting needs delete, and a drag needs rename
inside a folder or move between folders. An account can therefore be perfectly
able to write and still be unable to delete. The refusal says which one it was.

**A large file will not copy on Windows.** That is the 50 MB WebClient ceiling.
Raise `FileSizeLimitInBytes` as above, or send the file through the web
interface, where the transfer is resumable and unlimited.

**A big copy of many small files stalls or errors part way.** Everything except
reading counts against a write rate limit of 600 requests a minute, counted per
account when a token is used and per address otherwise. A mounted drive spends
those quickly, because it sends a PROPFIND and often a LOCK for every item as
well as the PUT. It clears itself within the minute. Use rclone with a modest
`--transfers` for large batches.

**A file appears then vanishes, or a save fails halfway.** Some editors write a
temporary file next to the original and rename it over the top. That works, but
it doubles the traffic for every save. Editing a large file directly on a mount
is slower than opening it in the Storix editor, which reads and writes once.

**Nothing at all responds at `/dav/`.** The tree stays closed until the first
run wizard has been completed, and answers 503 until then. Check
`GET /api/v1/system/status` and finish the setup first.

**Check what actually happened.** Open Settings, Security log, and look for
`webdav.denied`. A refused write carries the account it was refused for; a
refused sign in carries only the address, because nobody was identified. From
the shell:

```bash
sudo journalctl -u storix -f
sudo tail -f /var/log/storix/storix.log
```
