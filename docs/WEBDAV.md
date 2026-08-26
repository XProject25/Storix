# Mounting Storix as a drive

Storix serves the folders you already have access to over WebDAV, so they can
be opened from Windows Explorer, the macOS Finder or a Linux file manager and
worked with like any other drive. Nothing is copied to your machine, and the
same containment, permissions and audit trail apply as in the browser.

Developed by X Project.

## Before you start

**The address** is your Storix address followed by `/dav/`, for example
`https://files.example.com/dav/` or `http://185.12.34.56:8686/dav/`. The top
level of that address lists the folders your account owns, one collection each.

**The credentials** are your Storix username and an access token as the
password. Create a token in the interface, on the access tokens panel, and copy
it when it is shown. It is never shown again. A token with the `read` scope
mounts read only, whatever the account is otherwise allowed to do.

**Use HTTPS if you can.** Point a domain at the server, enter it in Settings,
Domain and HTTPS, and a certificate is issued for you. Every operating system
treats a WebDAV mount over plain HTTP as a downgrade, and Windows refuses one
outright until a registry value is changed.

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
credentials in the secrets file, which only root may read:

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

### rclone

rclone is the better tool when you want to copy and sync rather than browse,
and it does not need root. Create the remote in one command:

```bash
rclone config create storix webdav \
  url=http://SERVER:8686/dav/ \
  vendor=other \
  user=alice \
  pass="$(rclone obscure 'PASTE_TOKEN')"
```

That writes the following into `~/.config/rclone/rclone.conf`, which you can
also create by hand. The `pass` value is the obscured token, not the token
itself:

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
rclone copy /home/alice/backups storix:/var/backups --progress
```

To have it as a folder as well:

```bash
sudo mkdir -p /mnt/storix
rclone mount storix: /mnt/storix --vfs-cache-mode writes --daemon
```

## Troubleshooting

**It asks for the password over and over.** On Windows this is almost always
`BasicAuthLevel`, described above. Everywhere else, check that you are giving
the access token as the password rather than the account password, that the
token has not expired, and that it has not been revoked. The Security log in
Settings records every attempt with the address it came from.

**Everything is read only.** Three things can cause it, in this order. The
token has the `read` scope, so create a `write` one. The folder is mounted read
only for your account, which an administrator sets per folder. Or the account
does not carry the upload, create or delete permissions. The browser shows the
same restriction, which is a quick way to tell whether the mount is the problem
or the account is.

**A large file will not copy on Windows.** That is the 50 MB WebClient ceiling.
Raise `FileSizeLimitInBytes` as above, or send the file through the web
interface, where the transfer is resumable and unlimited.

**A file appears then vanishes, or a save fails halfway.** Some editors write a
temporary file next to the original and rename it over the top. That works, but
it doubles the traffic for every save. Editing a large file directly on a mount
is slower than opening it in the Storix editor, which reads and writes once.

**Nothing at all responds at `/dav/`.** The tree stays closed until the first
run wizard has been completed. Check `GET /api/v1/system/status` and finish the
setup first.

**Check what actually happened.** Every WebDAV operation is audited like any
other. Open Settings, Security log, and filter by your username. From the
shell:

```bash
sudo journalctl -u storix -f
sudo tail -f /var/log/storix/storix.log
```

## Notes and limits

- Locks are held in memory. Windows and macOS take a lock before writing, so
  Storix answers `LOCK`, but a restart of the service releases every lock. This
  matters only if two people edit the same file at the same moment.
- A mount reaches exactly the folders your account owns, and nothing else. The
  protected locations refused in the browser are refused here too.
- WebDAV carries no resumable upload. A transfer interrupted at 79 GB starts
  again from zero, which is what the tus upload in the web interface exists to
  avoid. Use the browser, or rclone, for very large transfers.
- Thumbnails, previews, share links, the transfer queue and the editor are
  features of the interface. A mount gives you the files themselves.
