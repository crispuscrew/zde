{ hypridle }:
# Supply source-ordered generations and wait for each bounded callback, so a
# delayed timeout cannot arrive after the resume that superseded it.
hypridle.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ [ ./hypridle-event-generation.patch ];
})
