#!/bin/bash
set -e

# Load environment variables
if [ -f /config/.env ]; then
    source /config/.env
fi

# If MODE is set, run specific command
case "${MODE:-}" in
    check)
        # Run check command
        exec yamisskey-doctor check ${CHECK_URL:-} ${CHECK_ARGS:-}
        ;;
    verify)
        # Run verify command once
        exec yamisskey-doctor verify --latest ${VERIFY_ARGS:-}
        ;;
    restore)
        # Run restore command
        exec yamisskey-doctor restore ${RESTORE_ARGS:-}
        ;;
    repair)
        # Run repair command
        exec yamisskey-doctor repair ${REPAIR_ARGS:-}
        ;;
    cron)
        # Setup cron for scheduled verify
        echo "Setting up cron for scheduled verify..."

        # Expand environment variables in crontab template
        envsubst < /etc/cron.d/crontab.template > /etc/cron.d/yamisskey-doctor
        chmod 0644 /etc/cron.d/yamisskey-doctor
        crontab /etc/cron.d/yamisskey-doctor

        # Start cron
        echo "Starting cron..."
        cron

        # Follow log
        tail -f /var/log/cron.log
        ;;
    *)
        # Default: run any command passed as arguments
        if [ $# -gt 0 ]; then
            exec yamisskey-doctor "$@"
        else
            echo "Usage: docker run yamisskey-doctor <command> [args]"
            echo ""
            echo "Commands:"
            echo "  check    - Check Misskey API/Streaming health"
            echo "  verify   - Verify backup can be restored"
            echo "  restore  - Restore database from backup"
            echo "  repair   - Repair database inconsistencies"
            echo ""
            echo "Environment variables:"
            echo "  MODE=check|verify|restore|repair|cron"
            echo "  CHECK_URL=https://example.com"
            echo "  MISSKEY_TOKEN=xxx"
            echo ""
            echo "For cron mode, set MODE=cron"
            exec yamisskey-doctor --help
        fi
        ;;
esac
