# Surveillance Marathon Lille

Bot Go autonome qui surveille la page Njuko du Marathon de Lille 2026 et envoie une notification Discord en DM quand la page change.

URL surveillee par defaut: <https://in.njuko.com/marathondelille2026>

- Verification automatique toutes les 10 minutes avec `time.Ticker`.
- Hash SHA256 de la page entiere.
- Persistance du dernier hash dans `last_hash.txt`.
- Abonnement simple avec `!subscribe`.
- Verification manuelle avec `!check`.
- Statut rapide avec `!status`.
- Notifications en DM via `discordgo.UserChannelCreate`.
- Configuration par `config.json`.
- Persistance des abonnes dans `subscribers.json`.

## Installation

Prerequis:

- Go 1.22 ou plus recent.
- Un bot Discord avec un token.

Installez les dependances:

```bash
go mod tidy
```

Copiez la configuration d'exemple:

```bash
cp config.example.json config.json
```

Editez `config.json`:

```json
{
  "discord_bot_token": "VOTRE_TOKEN_DISCORD_ICI",
  "default_user_id": "",
  "watch_url": "https://in.njuko.com/marathondelille2026",
  "check_interval_minutes": 10,
  "last_hash_file": "last_hash.txt",
  "subscribers_file": "subscribers.json"
}
```

`default_user_id` est optionnel. L'usage le plus simple reste d'envoyer `!subscribe` au bot.

## Lancement

En developpement:

```bash
go run .
```

Build:

```bash
go build -o surveillance-marathon-lille .
./surveillance-marathon-lille
```

Au premier passage, le bot initialise `last_hash.txt` sans envoyer d'alerte. Ensuite, chaque changement de hash declenche une notification DM aux abonnes.

## Commandes Discord

- `!subscribe`: abonne l'utilisateur aux notifications et tente d'envoyer un DM de confirmation.
- `!check`: lance une verification immediate.
- `!status`: affiche l'URL surveillee, le nombre d'abonnes, l'intervalle et le hash courant.
- `!help`: affiche les commandes.

## Creation du bot Discord

1. Allez sur <https://discord.com/developers/applications>.
2. Cliquez sur **New Application**.
3. Ouvrez **Bot**, puis **Add Bot**.
4. Copiez le token du bot et mettez-le dans `config.json`.
5. Dans **Bot > Privileged Gateway Intents**, activez **Message Content Intent**.
6. Dans **OAuth2 > URL Generator**:
   - Scopes: `bot`
   - Bot Permissions: `Send Messages`, `Read Message History`
7. Ouvrez l'URL generee pour inviter le bot sur un serveur.

Note Discord: le code utilise bien `UserChannelCreate` pour ouvrir un DM. Discord peut quand meme refuser un DM si l'utilisateur bloque les messages prives, si ses reglages de confidentialite l'interdisent, ou si le bot n'a aucun contexte autorise avec cet utilisateur. Le plus fiable est que l'utilisateur lance lui-meme `!subscribe` en serveur ou en DM.

## Lancer en continu

Avec `screen`:

```bash
screen -S marathon-lille
./surveillance-marathon-lille
```

Detacher la session: `Ctrl+A`, puis `D`.

Revenir dessus:

```bash
screen -r marathon-lille
```

Avec `systemd`, creez `/etc/systemd/system/surveillance-marathon-lille.service`:

```ini
[Unit]
Description=Surveillance Marathon Lille Discord Bot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/chemin/vers/Surveillance-Marathon-Lille
ExecStart=/chemin/vers/Surveillance-Marathon-Lille/surveillance-marathon-lille
Restart=always
RestartSec=10
User=votre_utilisateur

[Install]
WantedBy=multi-user.target
```

Puis:

```bash
sudo systemctl daemon-reload
sudo systemctl enable surveillance-marathon-lille
sudo systemctl start surveillance-marathon-lille
sudo journalctl -u surveillance-marathon-lille -f
```

## Lancer en continu sur Devuan

Devuan utilise souvent SysVinit au lieu de systemd. Apres avoir compile le bot:

```bash
go build -o surveillance-marathon-lille .
```

Placez le projet dans un dossier stable, par exemple:

```bash
sudo mkdir -p /opt/surveillance-marathon-lille
sudo cp surveillance-marathon-lille config.json /opt/surveillance-marathon-lille/
```

Creez `/etc/init.d/surveillance-marathon-lille`:

```sh
#!/bin/sh
### BEGIN INIT INFO
# Provides:          surveillance-marathon-lille
# Required-Start:    $network
# Required-Stop:     $network
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: Bot Discord Marathon de Lille
### END INIT INFO

DAEMON="/opt/surveillance-marathon-lille/surveillance-marathon-lille"
WORKDIR="/opt/surveillance-marathon-lille"
PIDFILE="/var/run/surveillance-marathon-lille.pid"
LOGFILE="/var/log/surveillance-marathon-lille.log"

case "$1" in
  start)
    echo "Demarrage de surveillance-marathon-lille"
    start-stop-daemon --start --background --make-pidfile --pidfile "$PIDFILE" \
      --chdir "$WORKDIR" --startas /bin/sh -- -c "exec $DAEMON >> $LOGFILE 2>&1"
    ;;
  stop)
    echo "Arret de surveillance-marathon-lille"
    start-stop-daemon --stop --pidfile "$PIDFILE" --retry 10
    rm -f "$PIDFILE"
    ;;
  restart)
    "$0" stop
    "$0" start
    ;;
  status)
    start-stop-daemon --status --pidfile "$PIDFILE"
    ;;
  *)
    echo "Usage: /etc/init.d/surveillance-marathon-lille {start|stop|restart|status}"
    exit 1
    ;;
esac

exit 0
```

Activez puis lancez le service:

```bash
sudo chmod +x /etc/init.d/surveillance-marathon-lille
sudo update-rc.d surveillance-marathon-lille defaults
sudo service surveillance-marathon-lille start
sudo service surveillance-marathon-lille status
```

Voir les logs:

```bash
tail -f /var/log/surveillance-marathon-lille.log
```
