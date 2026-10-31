package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/spf13/cobra"
)

// OwnerCredentials is what `bottown admin owner-credentials` hands to the
// host over `docker exec`.
type OwnerCredentials struct {
	Login    string `json:"login"`
	Password string `json:"password,omitempty"`
	APIKey   string `json:"api_key"`
}

// newAdmin holds the commands the host runs through `docker exec`; access to
// the Docker socket is the trust root.
func newAdmin() *cobra.Command {
	admin := &cobra.Command{
		Use:    "admin",
		Short:  "Container-side administration (run via docker exec)",
		Hidden: true,
	}
	owner := &cobra.Command{
		Use:   "owner-credentials",
		Short: "Mint an owner API key and print the owner's Forgejo password",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := readContainerEnv()
			if err != nil {
				return err
			}
			store, err := secrets.Open(e.Data)
			if err != nil {
				return err
			}
			st, err := store.Read()
			if err != nil {
				return err
			}
			key, err := store.NewAPIKey(e.Owner.Login, "owner")
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(OwnerCredentials{
				Login: e.Owner.Login, Password: st.OwnerPassword, APIKey: key,
			})
		},
	}

	var label string
	actorKey := &cobra.Command{
		Use:   "actor-key ACTOR",
		Short: "Mint a bottown API key for an actor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := readContainerEnv()
			if err != nil {
				return err
			}
			rec, store, err := newReconciler(e, nil)
			if err != nil {
				return err
			}
			b, ok := rec.Desired.Actor(args[0])
			switch {
			case args[0] == rec.Desired.Manager.Name:
				return fmt.Errorf("%q acts only inside bottown and never gets an API key", args[0])
			case !ok:
				return fmt.Errorf("unknown actor %q (actors: %s)", args[0], strings.Join(rec.Desired.ActorNames(), ", "))
			}
			key, err := store.NewAPIKey(b.Name, label)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), key)
			return nil
		},
	}
	actorKey.Flags().StringVar(&label, "label", "", "free-form note stored with the key")
	admin.AddCommand(owner, actorKey)
	return admin
}
